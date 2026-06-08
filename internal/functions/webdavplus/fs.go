package webdavplus

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/tickstep/cloudpan189-api/cloudpan"
	"github.com/tickstep/cloudpan189-api/cloudpan/apierror"
	"github.com/tickstep/library-go/requester"
	"golang.org/x/net/webdav"
)

type FS struct {
	Client   *cloudpan.PanClient
	FamilyId int64
}

type FileInfo struct{ E *cloudpan.AppFileEntity }

func (fi FileInfo) Name() string {
	if fi.E == nil || fi.E.FileName == "" {
		return "/"
	}
	return fi.E.FileName
}
func (fi FileInfo) Size() int64 {
	if fi.E == nil || fi.E.IsFolder {
		return 0
	}
	return fi.E.FileSize
}
func (fi FileInfo) Mode() os.FileMode {
	if fi.E != nil && fi.E.IsFolder {
		return os.ModeDir | 0755
	}
	return 0644
}
func (fi FileInfo) ModTime() time.Time {
	if fi.E == nil {
		return time.Now()
	}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*60*60)
	}
	for _, s := range []string{fi.E.LastOpTime, fi.E.CreateTime} {
		if s == "" {
			continue
		}
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, loc); err == nil {
			return t
		}
	}
	return time.Now()
}
func (fi FileInfo) IsDir() bool      { return fi.E != nil && fi.E.IsFolder }
func (fi FileInfo) Sys() interface{} { return fi.E }

type DirFile struct {
	fs     *FS
	name   string
	info   os.FileInfo
	offset int
}

func (d *DirFile) Close() error               { return nil }
func (d *DirFile) Read(p []byte) (int, error) { return 0, io.EOF }
func (d *DirFile) Seek(offset int64, whence int) (int64, error) {
	return 0, errors.New("seek on directory")
}
func (d *DirFile) Write(p []byte) (int, error) { return 0, errors.New("write on directory") }
func (d *DirFile) Stat() (os.FileInfo, error)  { return d.info, nil }
func (d *DirFile) Readdir(count int) ([]os.FileInfo, error) {
	ent, err := d.fs.entity(d.name)
	if err != nil {
		return nil, err
	}
	param := cloudpan.NewAppFileListParam()
	param.FamilyId = d.fs.FamilyId
	param.FileId = ent.FileId
	param.PageSize = 1000
	res, apiErr := d.fs.Client.AppFileList(param)
	if apiErr != nil {
		return nil, apiErr
	}
	out := make([]os.FileInfo, 0, len(res.FileList))
	for _, e := range res.FileList {
		out = append(out, FileInfo{E: e})
	}
	if count <= 0 {
		return out, nil
	}
	if d.offset >= len(out) {
		return nil, io.EOF
	}
	end := d.offset + count
	if end > len(out) {
		end = len(out)
	}
	part := out[d.offset:end]
	d.offset = end
	return part, nil
}

type ReadFile struct {
	fs          *FS
	ent         *cloudpan.AppFileEntity
	info        os.FileInfo
	rc          io.ReadCloser
	downloadURL string
	pos         int64
	size        int64
}

func (f *ReadFile) Close() error {
	if f.rc != nil {
		return f.rc.Close()
	}
	return nil
}
func (f *ReadFile) Read(p []byte) (int, error) {
	if f.pos >= f.size {
		return 0, io.EOF
	}
	if f.rc == nil {
		if err := f.openAt(f.pos); err != nil {
			return 0, err
		}
	}
	n, err := f.rc.Read(p)
	f.pos += int64(n)
	return n, err
}
func (f *ReadFile) Write(p []byte) (int, error) { return 0, errors.New("read-only file") }
func (f *ReadFile) Readdir(count int) ([]os.FileInfo, error) {
	return nil, errors.New("not a directory")
}
func (f *ReadFile) Stat() (os.FileInfo, error) { return f.info, nil }
func (f *ReadFile) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = f.pos + offset
	case io.SeekEnd:
		abs = f.size + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("negative position")
	}
	if f.rc != nil {
		_ = f.rc.Close()
		f.rc = nil
	}
	f.pos = abs
	return abs, nil
}

func (f *ReadFile) openAt(offset int64) error {
	rc, err := f.fs.openDownload(f.ent, &f.downloadURL, offset)
	if err != nil {
		return err
	}
	f.rc = rc
	return nil
}

type writeFileInfo struct {
	name    string
	size    int64
	modTime time.Time
}

func (fi writeFileInfo) Name() string       { return fi.name }
func (fi writeFileInfo) Size() int64        { return fi.size }
func (fi writeFileInfo) Mode() os.FileMode  { return 0644 }
func (fi writeFileInfo) ModTime() time.Time { return fi.modTime }
func (fi writeFileInfo) IsDir() bool        { return false }
func (fi writeFileInfo) Sys() interface{}   { return nil }

type WriteFile struct {
	fs       *FS
	name     string
	baseName string
	tmp      *os.File
	closed   bool
}

func (f *WriteFile) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	defer os.Remove(f.tmp.Name())
	defer f.tmp.Close()
	return f.fs.uploadTempFile(f.name, f.tmp)
}
func (f *WriteFile) Read(p []byte) (int, error) { return f.tmp.Read(p) }
func (f *WriteFile) Write(p []byte) (int, error) {
	if f.closed {
		return 0, os.ErrClosed
	}
	return f.tmp.Write(p)
}
func (f *WriteFile) Readdir(count int) ([]os.FileInfo, error) {
	return nil, errors.New("not a directory")
}
func (f *WriteFile) Stat() (os.FileInfo, error) {
	st, err := f.tmp.Stat()
	if err != nil {
		return nil, err
	}
	return writeFileInfo{name: f.baseName, size: st.Size(), modTime: st.ModTime()}, nil
}
func (f *WriteFile) Seek(offset int64, whence int) (int64, error) {
	return f.tmp.Seek(offset, whence)
}

func cleanName(name string) string {
	if name == "" {
		return "/"
	}
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	return path.Clean(name)
}

func (fsys *FS) entity(name string) (*cloudpan.AppFileEntity, error) {
	name = cleanName(name)
	if name == "/" {
		return cloudpan.NewAppFileEntityForRootDir(), nil
	}
	e, apiErr := fsys.Client.AppFileInfoByPath(fsys.FamilyId, name)
	if apiErr != nil {
		return nil, os.ErrNotExist
	}
	return e, nil
}

func (fsys *FS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	name = cleanName(name)
	if name == "/" {
		return os.ErrExist
	}
	parentPath, base := path.Split(strings.TrimRight(name, "/"))
	if parentPath == "" {
		parentPath = "/"
	}
	parent, err := fsys.entity(parentPath)
	if err != nil {
		return err
	}
	_, apiErr := fsys.Client.AppMkdir(fsys.FamilyId, parent.FileId, base)
	if apiErr != nil {
		return apiErr
	}
	return nil
}

func (fsys *FS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	name = cleanName(name)
	// x/net/webdav uses O_RDWR alone for PROPPATCH. Treat a file as writable only
	// when the caller is actually creating/truncating/appending or opening
	// write-only; this keeps PROPPATCH from accidentally uploading an empty file.
	if flag&(os.O_CREATE|os.O_TRUNC|os.O_APPEND|os.O_WRONLY) != 0 {
		if name == "/" {
			return nil, os.ErrPermission
		}
		parentPath, baseName := path.Split(name)
		if parentPath == "" {
			parentPath = "/"
		}
		parent, err := fsys.entity(parentPath)
		if err != nil {
			return nil, err
		}
		if !parent.IsFolder {
			return nil, os.ErrInvalid
		}
		if existed, err := fsys.entity(name); err == nil && existed.IsFolder {
			return nil, os.ErrPermission
		}
		tmp, err := os.CreateTemp("", "cloudpan189-go-webdav-put-*")
		if err != nil {
			return nil, err
		}
		return &WriteFile{fs: fsys, name: name, baseName: baseName, tmp: tmp}, nil
	}
	ent, err := fsys.entity(name)
	if err != nil {
		return nil, err
	}
	info := FileInfo{E: ent}
	if ent.IsFolder {
		return &DirFile{fs: fsys, name: name, info: info}, nil
	}
	return &ReadFile{fs: fsys, ent: ent, info: info, size: ent.FileSize}, nil
}

func (fsys *FS) RemoveAll(ctx context.Context, name string) error {
	name = cleanName(name)
	if name == "/" {
		return os.ErrPermission
	}
	ent, err := fsys.entity(name)
	if err != nil {
		return err
	}
	_, apiErr := fsys.Client.AppDeleteFile([]string{ent.FileId})
	if apiErr != nil {
		return apiErr
	}
	return nil
}

func (fsys *FS) Rename(ctx context.Context, oldName, newName string) error {
	oldName = cleanName(oldName)
	newName = cleanName(newName)
	if oldName == "/" || newName == "/" {
		return os.ErrPermission
	}
	ent, err := fsys.entity(oldName)
	if err != nil {
		return err
	}
	oldParent, _ := path.Split(strings.TrimRight(oldName, "/"))
	newParent, newBase := path.Split(strings.TrimRight(newName, "/"))
	if oldParent == newParent {
		_, apiErr := fsys.Client.AppRenameFile(ent.FileId, newBase)
		if apiErr != nil {
			return apiErr
		}
		return nil
	}
	dst, err := fsys.entity(newParent)
	if err != nil {
		return err
	}
	_, apiErr := fsys.Client.AppMoveFile([]string{ent.FileId}, dst.FileId)
	if apiErr != nil {
		return apiErr
	}
	if ent.FileName != newBase {
		_, apiErr = fsys.Client.AppRenameFile(ent.FileId, newBase)
		if apiErr != nil {
			return apiErr
		}
	}
	return nil
}

func (fsys *FS) openDownload(ent *cloudpan.AppFileEntity, cachedURL *string, offset int64) (io.ReadCloser, error) {
	if ent == nil || ent.IsFolder {
		return nil, os.ErrInvalid
	}
	var apiErr *apierror.ApiError
	if cachedURL == nil || *cachedURL == "" {
		var dl string
		if fsys.FamilyId > 0 {
			dl, apiErr = fsys.Client.AppFamilyGetFileDownloadUrl(fsys.FamilyId, ent.FileId)
		} else {
			dl, apiErr = fsys.Client.AppGetFileDownloadUrl(ent.FileId)
		}
		if apiErr != nil {
			return nil, apiErr
		}
		if cachedURL != nil {
			*cachedURL = dl
		}
	}
	downloadURL := ""
	if cachedURL != nil {
		downloadURL = *cachedURL
	}
	var resp *http.Response
	downloadFunc := func(method, url string, headers map[string]string) (*http.Response, error) {
		client := requester.NewHTTPClient()
		client.SetTimeout(0)
		r, err := client.Req(method, url, nil, headers)
		resp = r
		return r, err
	}
	rng := cloudpan.AppFileDownloadRange{Offset: offset}
	if fsys.FamilyId > 0 {
		if er := fsys.Client.AppFamilyDownloadFileData(downloadURL, rng, downloadFunc); er != nil {
			return nil, er
		}
	} else {
		if er := fsys.Client.AppDownloadFileData(downloadURL, rng, downloadFunc); er != nil {
			return nil, er
		}
	}
	if resp == nil {
		return nil, os.ErrInvalid
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, fmt.Errorf("download http status %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func (fsys *FS) uploadTempFile(name string, f *os.File) error {
	name = cleanName(name)
	st, err := f.Stat()
	if err != nil {
		return err
	}
	size := st.Size()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	md5Str := strings.ToUpper(hex.EncodeToString(h.Sum(nil)))
	if size == 0 {
		md5Str = cloudpan.DefaultEmptyFileMd5
	}

	if existed, err := fsys.entity(name); err == nil {
		if existed.IsFolder {
			return os.ErrPermission
		}
		if strings.EqualFold(existed.FileMd5, md5Str) && existed.FileSize == size {
			return nil
		}
		// The personal-cloud commit API supports overwrite. The family-cloud
		// helper in cloudpan189-api does not expose an overwrite flag, so delete
		// the existing family file before uploading to keep WebDAV PUT semantics.
		if fsys.FamilyId > 0 {
			if _, er := fsys.Client.AppDeleteFile([]string{existed.FileId}); er != nil {
				return er
			}
			time.Sleep(500 * time.Millisecond)
		}
	}

	parentPath, baseName := path.Split(name)
	if parentPath == "" {
		parentPath = "/"
	}
	parent, err := fsys.entity(parentPath)
	if err != nil {
		return err
	}
	if !parent.IsFolder {
		return os.ErrInvalid
	}

	param := &cloudpan.AppCreateUploadFileParam{
		FamilyId:       fsys.FamilyId,
		ParentFolderId: parent.FileId,
		FileName:       baseName,
		Size:           size,
		Md5:            md5Str,
		LastWrite:      st.ModTime().Format("2006-01-02 15:04:05"),
		LocalPath:      name,
	}
	var created *cloudpan.AppCreateUploadFileResult
	if fsys.FamilyId > 0 {
		var er *apierror.ApiError
		created, er = fsys.Client.AppFamilyCreateUploadFile(param)
		if er != nil {
			return er
		}
	} else {
		var er *apierror.ApiError
		created, er = fsys.Client.AppCreateUploadFile(param)
		if er != nil {
			return er
		}
	}

	if created.FileDataExists != 1 && size > 0 {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		uploadFunc := func(method, fullURL string, headers map[string]string) (*http.Response, error) {
			section := io.NewSectionReader(f, 0, size)
			req, err := http.NewRequest(method, fullURL, section)
			if err != nil {
				return nil, err
			}
			req.ContentLength = size
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			client := requester.NewHTTPClient()
			client.SetTimeout(0)
			return client.Do(req)
		}
		fileRange := &cloudpan.AppFileUploadRange{Offset: 0, Len: size}
		if fsys.FamilyId > 0 {
			if er := fsys.Client.AppFamilyUploadFileData(fsys.FamilyId, created.FileUploadUrl, created.UploadFileId, created.XRequestId, fileRange, uploadFunc); er != nil {
				return er
			}
		} else {
			if er := fsys.Client.AppUploadFileData(created.FileUploadUrl, created.UploadFileId, created.XRequestId, fileRange, uploadFunc); er != nil {
				return er
			}
		}
	}

	if fsys.FamilyId > 0 {
		_, er := fsys.Client.AppFamilyUploadFileCommit(fsys.FamilyId, created.FileCommitUrl, created.UploadFileId, created.XRequestId)
		if er != nil {
			return er
		}
	} else {
		_, er := fsys.Client.AppUploadFileCommitOverwrite(created.FileCommitUrl, created.UploadFileId, created.XRequestId, true)
		if er != nil {
			return er
		}
	}
	return nil
}

func (fsys *FS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	ent, err := fsys.entity(name)
	if err != nil {
		return nil, err
	}
	return FileInfo{E: ent}, nil
}

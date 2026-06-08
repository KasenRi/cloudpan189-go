package webdavplus

import (
	"github.com/tickstep/cloudpan189-api/cloudpan"
	"golang.org/x/net/webdav"
	"net/http"
)

func Serve(addr string, client *cloudpan.PanClient, familyId int64) error {
	fs := &FS{Client: client, FamilyId: familyId}
	handler := &webdav.Handler{FileSystem: fs, LockSystem: webdav.NewMemLS()}
	return http.ListenAndServe(addr, handler)
}

// Copyright (c) 2020 tickstep.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package config

import (
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/tickstep/cloudpan189-api/cloudpan"
	"github.com/tickstep/cloudpan189-api/cloudpan/apierror"
	"github.com/tickstep/library-go/expires/cachemap"
	"github.com/tickstep/library-go/logger"
	"path"
	"path/filepath"
)

type PanUser struct {
	UID               uint64                 `json:"uid"`
	Nickname          string                 `json:"nickname"`
	AccountName       string                 `json:"accountName"`
	Sex               string                 `json:"sex"`
	Workdir           string                 `json:"workdir"`
	WorkdirFileEntity cloudpan.AppFileEntity `json:"workdirFileEntity"`

	FamilyWorkdir           string                 `json:"familyWorkdir"`
	FamilyWorkdirFileEntity cloudpan.AppFileEntity `json:"familyWorkdirFileEntity"`

	ActiveFamilyId   int64                  `json:"activeFamilyId"` // 0代表个人云
	ActiveFamilyInfo cloudpan.AppFamilyInfo `json:"activeFamilyInfo"`

	LoginUserName     string `json:"loginUserName"`
	LoginUserPassword string `json:"loginUserPassword"`

	WebToken   cloudpan.WebLoginToken `json:"webToken"`
	AppToken   cloudpan.AppLoginToken `json:"appToken"`
	panClient  *cloudpan.PanClient
	cacheOpMap cachemap.CacheOpMap
}

type PanUserList []*PanUser

func SetupUserByCookie(webToken *cloudpan.WebLoginToken, appToken *cloudpan.AppLoginToken) (user *PanUser, err *apierror.ApiError) {
	tryRefreshWebToken := true

doLoginAct:
	panClient := cloudpan.NewPanClient(*webToken, *appToken)
	u := &PanUser{
		WebToken:                *webToken,
		AppToken:                *appToken,
		panClient:               panClient,
		Workdir:                 "/",
		WorkdirFileEntity:       *cloudpan.NewAppFileEntityForRootDir(),
		FamilyWorkdir:           "/",
		FamilyWorkdirFileEntity: *cloudpan.NewAppFileEntityForRootDir(),
	}

	// web api token maybe expired
	userInfo, err := panClient.GetUserInfo()
	if err != nil {
		if err.Code == apierror.ApiCodeTokenExpiredCode && appToken.SessionKey != "" && tryRefreshWebToken {
			tryRefreshWebToken = false
			webCookie := cloudpan.RefreshCookieToken(appToken.SessionKey)
			if webCookie != "" {
				webToken.CookieLoginUser = webCookie
				goto doLoginAct
			}
		}
		return nil, err
	}
	name := "Unknown"
	if userInfo != nil {
		name = userInfo.Nickname
		if name == "" {
			name = userInfo.UserAccount
		}

		// update cloudUser
		u.UID = userInfo.UserId
		u.AccountName = userInfo.UserAccount
	} else {
		// error, maybe the token has expired
		return nil, apierror.NewFailedApiError("cannot get user info, the token has expired")
	}
	u.Nickname = name

	userDetailInfo, err := panClient.GetUserDetailInfo()
	if userDetailInfo != nil {
		if userDetailInfo.Gender == "F" {
			u.Sex = "F"
		} else if userDetailInfo.Gender == "M" {
			u.Sex = "M"
		} else {
			u.Sex = "U"
		}
	} else {
		// error, maybe the token has expired
		return nil, apierror.NewFailedApiError("cannot get user info, the token has expired")
	}

	return u, nil
}

// IsUsableAppToken reports whether the token is enough for app API calls.
func IsUsableAppToken(appToken cloudpan.AppLoginToken) bool {
	return appToken.SessionKey != "" && appToken.SessionSecret != ""
}

// SetupUserByAppTokenFallback creates a user backed by app tokens only.
//
// 新版天翼登录有时不再能换取 COOKIE_LOGIN_USER，但 APP session 仍然可用。
// 这里不伪造 COOKIE_LOGIN_USER，只保留真实 app token，并使用稳定的本地 UID
// 避免 MaurUppi fork 中固定 UID=1 带来的多账号冲突。
func SetupUserByAppTokenFallback(accountName string, webToken *cloudpan.WebLoginToken, appToken *cloudpan.AppLoginToken) *PanUser {
	if appToken == nil || !IsUsableAppToken(*appToken) {
		return nil
	}
	if webToken == nil {
		webToken = &cloudpan.WebLoginToken{}
	}

	name := strings.TrimSpace(accountName)
	if name == "" {
		name = "AppTokenUser"
	}

	return &PanUser{
		UID:                     fallbackUIDForAppToken(name, *appToken),
		Nickname:                name,
		AccountName:             name,
		Sex:                     "U",
		Workdir:                 "/",
		WorkdirFileEntity:       *cloudpan.NewAppFileEntityForRootDir(),
		FamilyWorkdir:           "/",
		FamilyWorkdirFileEntity: *cloudpan.NewAppFileEntityForRootDir(),
		WebToken:                *webToken,
		AppToken:                *appToken,
		panClient:               cloudpan.NewPanClient(*webToken, *appToken),
	}
}

func fallbackUIDForAppToken(accountName string, appToken cloudpan.AppLoginToken) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.ToLower(strings.TrimSpace(accountName))))
	if accountName == "" {
		_, _ = h.Write([]byte(appToken.SessionKey))
	}
	uid := h.Sum64()
	if uid == 0 {
		uid = 1
	}
	// Mark as local fallback UID and avoid colliding with real service UID space.
	return uid | (uint64(1) << 63)
}

func (pu *PanUser) PanClient() *cloudpan.PanClient {
	return pu.panClient
}

// PathJoin 合并工作目录和相对路径p, 若p为绝对路径则忽略
func (pu *PanUser) PathJoin(familyId int64, p string) string {
	if path.IsAbs(p) {
		return p
	}
	if familyId > 0 {
		if familyId == pu.ActiveFamilyId {
			return path.Join(pu.FamilyWorkdir, p)
		} else {
			return path.Join("/", p)
		}
	} else {
		return path.Join(pu.Workdir, p)
	}
}

func (pu *PanUser) FreshWorkdirInfo() {
	fe, err := pu.PanClient().AppFileInfoById(pu.ActiveFamilyId, pu.WorkdirFileEntity.FileId)
	if err != nil {
		logger.Verboseln("刷新工作目录信息失败")
		return
	}
	if pu.ActiveFamilyId > 0 {
		pu.FamilyWorkdirFileEntity = *fe
	} else {
		pu.WorkdirFileEntity = *fe
	}
}

// GetSavePath 根据提供的网盘文件路径 panpath, 返回本地储存路径,
// 返回绝对路径, 获取绝对路径出错时才返回相对路径...
func (pu *PanUser) GetSavePath(filePanPath string) string {
	dirStr := filepath.Join(Config.SaveDir, fmt.Sprintf("%d", pu.UID), filePanPath)
	dir, err := filepath.Abs(dirStr)
	if err != nil {
		dir = filepath.Clean(dirStr)
	}
	return dir
}

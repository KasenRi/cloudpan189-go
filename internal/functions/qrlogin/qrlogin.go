package qrlogin

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tickstep/cloudpan189-api/cloudpan"
	"github.com/tickstep/cloudpan189-api/cloudpan/apiutil"
	"github.com/tickstep/library-go/requester"
)

const (
	unifyLoginURL = "https://cloud.189.cn/unifyLoginForPC.action"
	authBaseURL   = "https://open.e.189.cn/api/logbox/oauth2"
	apiBaseURL    = "https://api.cloud.189.cn"
)

type loginContent struct {
	http    *requester.HTTPClient
	referer string
	appKey  string
	reqID   string
	lt      string
}

type appConf struct {
	Data struct {
		AccountType string `json:"accountType"`
		AppKey      string `json:"appKey"`
		ClientType  int    `json:"clientType"`
		IsOauth2    bool   `json:"isOauth2"`
		MailSuffix  string `json:"mailSuffix"`
		ParamID     string `json:"paramId"`
		ReqID       string `json:"reqId"`
		ReturnURL   string `json:"returnUrl"`
	} `json:"data"`
	Msg    string      `json:"msg"`
	Result interface{} `json:"result"`
}

type uuidResp struct {
	UUID       string `json:"uuid,omitempty"`
	EncryUUID  string `json:"encryuuid,omitempty"`
	EncodeUUID string `json:"encodeuuid,omitempty"`
}

type qrCodeState struct {
	RedirectURL string `json:"redirectUrl,omitempty"`
	Status      int32  `json:"status,omitempty"`
	Msg         string `json:"msg,omitempty"`
	SSON        string
}

type sessionResp struct {
	ResCode             int    `json:"res_code"`
	ResMessage          string `json:"res_message"`
	AccessToken         string `json:"accessToken"`
	FamilySessionKey    string `json:"familySessionKey"`
	FamilySessionSecret string `json:"familySessionSecret"`
	LoginName           string `json:"loginName"`
	RefreshToken        string `json:"refreshToken"`
	SessionKey          string `json:"sessionKey"`
	SessionSecret       string `json:"sessionSecret"`
}

type AppUserInfo struct {
	UID       uint64
	LoginName string
	Nickname  string
}

type Result struct {
	WebToken  cloudpan.WebLoginToken
	AppToken  cloudpan.AppLoginToken
	UserInfo  AppUserInfo
	ImageURL  string
	SSON      string
	LoginName string
}

func beforeLoginParams() url.Values {
	params := url.Values{}
	params.Set("appId", "9317140619")
	params.Set("clientType", "10020")
	params.Set("timeStamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	params.Set("returnURL", "https://m.cloud.189.cn/zhuanti/2020/loginErrorPc/index.html")
	return params
}

func prepareLogin(client *requester.HTTPClient) (*loginContent, error) {
	u := unifyLoginURL + "?" + beforeLoginParams().Encode()
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Req(req.Method, req.URL.String(), nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	referer := resp.Request.URL.String()
	if resp.Request.Response != nil && resp.Request.Response.Header.Get("Location") != "" {
		referer = resp.Request.Response.Header.Get("Location")
	}
	if referer == "" {
		referer = resp.Request.URL.String()
	}
	if strings.HasPrefix(referer, "/") {
		base, _ := url.Parse(unifyLoginURL)
		ref, _ := url.Parse(referer)
		referer = base.ResolveReference(ref).String()
	}
	refURL, err := url.Parse(referer)
	if err != nil {
		return nil, err
	}
	q := refURL.Query()
	return &loginContent{
		http:    client,
		referer: referer,
		appKey:  q.Get("appId"),
		reqID:   q.Get("reqId"),
		lt:      q.Get("lt"),
	}, nil
}

func (lc *loginContent) getAppConf() (*appConf, error) {
	form := map[string]string{
		"version": "2.0",
		"appKey":  lc.appKey,
	}
	headers := map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
		"Origin":       "https://open.e.189.cn",
		"Referer":      lc.referer,
		"Reqid":        lc.reqID,
		"lt":           lc.lt,
	}
	resp, err := lc.http.Req(http.MethodPost, authBaseURL+"/appConf.do", form, headers)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	conf := &appConf{}
	if err := json.Unmarshal(body, conf); err != nil {
		return nil, err
	}
	if conf.Data.AppKey == "" {
		return nil, fmt.Errorf("获取扫码登录配置失败: %s", string(body))
	}
	return conf, nil
}

func (lc *loginContent) getUUID() (*uuidResp, string, error) {
	u, _ := url.Parse(authBaseURL + "/getUUID.do")
	q := u.Query()
	q.Set("appId", lc.appKey)
	u.RawQuery = q.Encode()
	resp, err := lc.http.Req(http.MethodGet, u.String(), nil, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	ur := &uuidResp{}
	if err := json.Unmarshal(body, ur); err != nil {
		return nil, "", err
	}
	if ur.UUID == "" {
		return nil, "", fmt.Errorf("获取二维码 UUID 失败: %s", string(body))
	}
	u, _ = url.Parse(authBaseURL + "/image.do")
	q = u.Query()
	q.Set("REQID", lc.reqID)
	imageUUID, _ := url.PathUnescape(ur.EncodeUUID)
	q.Set("uuid", imageUUID)
	u.RawQuery = q.Encode()
	return ur, u.String(), nil
}

func (lc *loginContent) queryQRCode(conf *appConf, ur *uuidResp) (*qrCodeState, error) {
	u, _ := url.Parse(authBaseURL + "/qrcodeLoginState.do")
	q := u.Query()
	q.Set("appId", conf.Data.AppKey)
	q.Set("encryuuid", ur.EncryUUID)
	q.Set("date", time.Now().Format("2006-01-0215:04:059"))
	q.Set("uuid", ur.UUID)
	q.Set("returnUrl", conf.Data.ReturnURL)
	q.Set("clientType", strconv.Itoa(conf.Data.ClientType))
	q.Set("timeStamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	q.Set("cb_SaveName", "0")
	q.Set("isOauth2", strconv.FormatBool(conf.Data.IsOauth2))
	q.Set("state", "")
	q.Set("paramId", conf.Data.ParamID)
	u.RawQuery = q.Encode()
	headers := map[string]string{
		"Referer": lc.referer,
	}
	resp, err := lc.http.Req(http.MethodPost, u.String(), nil, headers)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	state := &qrCodeState{}
	if err := json.Unmarshal(body, state); err != nil {
		return nil, err
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "SSON" {
			state.SSON = ck.Value
			break
		}
	}
	return state, nil
}

func getSession(client *requester.HTTPClient, redirectURL string) (*sessionResp, error) {
	form := map[string]string{"redirectURL": redirectURL}
	headers := map[string]string{
		"Accept":       "application/json;charset=UTF-8",
		"Content-Type": "application/x-www-form-urlencoded",
	}
	u := apiBaseURL + "/getSessionForPC.action?" + apiutil.PcClientInfoSuffixParam()
	resp, err := client.Req(http.MethodPost, u, form, headers)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	session := &sessionResp{}
	if err := json.Unmarshal(body, session); err != nil {
		return nil, err
	}
	if session.ResCode != 0 || session.SessionKey == "" || session.SessionSecret == "" {
		return nil, fmt.Errorf("获取 session 失败: %s", string(body))
	}
	return session, nil
}

func fetchAppUserInfo(client *requester.HTTPClient, token cloudpan.AppLoginToken, loginName string) (AppUserInfo, error) {
	u := apiBaseURL + "/getUserInfo.action?" + apiutil.PcClientInfoSuffixParam()
	method := http.MethodGet
	date := apiutil.DateOfGmtStr()
	headers := map[string]string{
		"Date":         date,
		"SessionKey":   token.SessionKey,
		"Signature":    apiutil.SignatureOfHmac(token.SessionSecret, token.SessionKey, method, u, date),
		"X-Request-ID": apiutil.XRequestId(),
	}
	resp, err := client.Req(method, u, nil, headers)
	if err != nil {
		return AppUserInfo{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	type userXML struct {
		LoginName string `xml:"loginName"`
	}
	ux := &userXML{}
	_ = xml.Unmarshal(body, ux)
	if ux.LoginName == "" {
		ux.LoginName = loginName
	}
	uid := parseUID(ux.LoginName)
	if uid == 0 {
		return AppUserInfo{}, fmt.Errorf("无法从登录名解析 uid: %s", ux.LoginName)
	}
	return AppUserInfo{
		UID:       uid,
		LoginName: ux.LoginName,
		Nickname:  strings.TrimSuffix(ux.LoginName, "@189.cn"),
	}, nil
}

func parseUID(loginName string) uint64 {
	name := strings.TrimSpace(loginName)
	if i := strings.Index(name, "@"); i >= 0 {
		name = name[:i]
	}
	if name == "" {
		return 0
	}
	uid, _ := strconv.ParseUint(name, 10, 64)
	return uid
}

// Login performs QR-code login. The onImageURL callback is called as soon as
// the image URL is available, so callers can print or display it while polling.
func Login(timeout time.Duration, onImageURL func(string)) (*Result, error) {
	client := requester.NewHTTPClient()
	client.SetTimeout(0)

	lc, err := prepareLogin(client)
	if err != nil {
		return nil, err
	}
	conf, err := lc.getAppConf()
	if err != nil {
		return nil, err
	}
	ur, imageURL, err := lc.getUUID()
	if err != nil {
		return nil, err
	}
	if onImageURL != nil {
		onImageURL(imageURL)
	}

	deadline := time.Now().Add(timeout)
	if timeout <= 0 {
		deadline = time.Now().Add(3 * time.Minute)
	}
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		state, err := lc.queryQRCode(conf, ur)
		if err != nil {
			return nil, err
		}
		switch state.Status {
		case -106:
			fmt.Println("二维码待扫描...")
		case -11002:
			fmt.Println("已扫码，等待手机确认...")
		case 0:
			fmt.Println("扫码确认成功，正在获取 session...")
			session, err := getSession(client, state.RedirectURL)
			if err != nil {
				return nil, err
			}
			appToken := cloudpan.AppLoginToken{
				SessionKey:          session.SessionKey,
				SessionSecret:       session.SessionSecret,
				FamilySessionKey:    session.FamilySessionKey,
				FamilySessionSecret: session.FamilySessionSecret,
				AccessToken:         session.AccessToken,
				RefreshToken:        session.RefreshToken,
			}
			userInfo, err := fetchAppUserInfo(client, appToken, session.LoginName)
			if err != nil {
				return nil, err
			}
			return &Result{
				AppToken:  appToken,
				UserInfo:  userInfo,
				ImageURL:  imageURL,
				SSON:      state.SSON,
				LoginName: session.LoginName,
			}, nil
		default:
			return nil, fmt.Errorf("扫码登录状态异常: status=%d msg=%s", state.Status, state.Msg)
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("扫码登录超时")
		}
		<-ticker.C
	}
}

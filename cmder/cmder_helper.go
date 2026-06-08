package cmder

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tickstep/cloudpan189-api/cloudpan"
	"github.com/tickstep/cloudpan189-api/cloudpan/apierror"
	"github.com/tickstep/cloudpan189-go/cmder/cmdliner"
	"github.com/tickstep/cloudpan189-go/internal/config"
	"github.com/tickstep/library-go/logger"
	"github.com/urfave/cli"
)

var (
	appInstance *cli.App

	saveConfigMutex *sync.Mutex = new(sync.Mutex)

	ReloadConfigFunc = func(c *cli.Context) error {
		err := config.Config.Reload()
		if err != nil {
			fmt.Printf("重载配置错误: %s\n", err)
		}
		return nil
	}

	SaveConfigFunc = func(c *cli.Context) error {
		saveConfigMutex.Lock()
		defer saveConfigMutex.Unlock()
		err := config.Config.Save()
		if err != nil {
			fmt.Printf("保存配置错误: %s\n", err)
		}
		return nil
	}
)

func SetApp(app *cli.App) {
	appInstance = app
}

func App() *cli.App {
	return appInstance
}

func DoLoginHelper(username, password string) (usernameStr, passwordStr string, webToken cloudpan.WebLoginToken, appToken cloudpan.AppLoginToken, err error) {
	line := cmdliner.NewLiner()
	defer func() {
		_ = line.Close()
		if r := recover(); r != nil {
			err = fmt.Errorf("登录过程发生严重错误: %v", r)
		}
	}()

	if username == "" {
		username, err = line.State.Prompt("请输入用户名(手机号/邮箱/别名), 回车键提交 > ")
		if err != nil {
			return
		}
	}
	username = strings.TrimSpace(username)

	if password == "" {
		// liner 的 PasswordPrompt 不安全, 拆行之后密码就会显示出来了
		fmt.Printf("请输入密码(输入的密码无回显, 确认输入完成, 回车提交即可) > ")
		password, err = line.State.PasswordPrompt("")
		if err != nil {
			return
		}
	}

	if username == "" {
		return "", "", webToken, appToken, fmt.Errorf("用户名不能为空")
	}
	if strings.TrimSpace(password) == "" {
		return "", "", webToken, appToken, fmt.Errorf("密码不能为空")
	}

	atoken, loginErr := appLoginWithRetry(username, password, 3)
	if loginErr != nil {
		return "", "", webToken, appToken, loginErr
	}
	fmt.Println("APP 登录成功")

	// web cookie
	wtoken := &cloudpan.WebLoginToken{}
	cookieLoginUser := cloudpan.RefreshCookieToken(atoken.SessionKey)
	if cookieLoginUser != "" {
		logger.Verboseln("get COOKIE_LOGIN_USER by session key")
		wtoken.CookieLoginUser = cookieLoginUser
	} else {
		logger.Verboseln("refresh COOKIE_LOGIN_USER by app session failed, try web login")
		webLoginToken, apiErr := webLoginWithCaptcha(line, username, password)
		if apiErr != nil {
			fmt.Printf("注意: 网页 Cookie 登录补齐失败（错误代码: %d，错误信息: %s）。\n", apiErr.Code, apiErr.Error())
			if config.IsUsableAppToken(*atoken) {
				fmt.Println("将仅保存 APP session；文件列表、上传、下载等 APP 接口仍可用，部分依赖网页 Cookie 的命令可能不可用。")
			} else {
				return "", "", webToken, appToken, apiErr
			}
		} else if webLoginToken != nil {
			wtoken = webLoginToken
		}
	}

	webToken = *wtoken
	appToken = *atoken
	usernameStr = username
	passwordStr = password
	return
}

func appLoginWithRetry(username, password string, maxRetries int) (*cloudpan.AppLoginToken, error) {
	if maxRetries < 1 {
		maxRetries = 1
	}
	var lastErr *apierror.ApiError
	for attempt := 1; attempt <= maxRetries; attempt++ {
		fmt.Printf("正在尝试 APP 登录... (第 %d/%d 次)\n", attempt, maxRetries)
		token, apiErr := safeAppLogin(username, password)
		if apiErr == nil && token != nil && config.IsUsableAppToken(*token) {
			return token, nil
		}
		if apiErr == nil {
			apiErr = apierror.NewFailedApiError("APP 登录返回了无效 session")
		}
		lastErr = apiErr
		fmt.Printf("第 %d 次 APP 登录失败 (错误代码: %d，错误信息: %s)\n", attempt, apiErr.Code, apiErr.Error())
		if attempt < maxRetries {
			wait := time.Duration(attempt) * time.Second
			fmt.Printf("等待 %s 后重试...\n", wait)
			time.Sleep(wait)
		}
	}
	if lastErr == nil {
		return nil, fmt.Errorf("APP 登录失败")
	}
	return nil, fmt.Errorf("APP 登录失败（已重试 %d 次，最后错误代码: %d）: %s", maxRetries, lastErr.Code, lastErr.Error())
}

func safeAppLogin(username, password string) (token *cloudpan.AppLoginToken, apiErr *apierror.ApiError) {
	defer func() {
		if r := recover(); r != nil {
			apiErr = apierror.NewFailedApiError(fmt.Sprintf("APP 登录时发生 panic: %v", r))
		}
	}()
	token, apiErr = cloudpan.AppLogin(username, password)
	return
}

func webLoginWithCaptcha(line *cmdliner.CmdLiner, username, password string) (wtoken *cloudpan.WebLoginToken, apiErr *apierror.ApiError) {
	wtoken, apiErr = safeWebLogin(username, password)
	if apiErr == nil {
		return wtoken, nil
	}
	if apiErr.Code != apierror.ApiCodeNeedCaptchaCode {
		return wtoken, apiErr
	}

	for i := 0; i < 10; i++ {
		savePath, captchaErr := cloudpan.GetCaptchaImage()
		if captchaErr != nil {
			return nil, captchaErr
		}
		fmt.Printf("打开以下路径, 以查看验证码\n%s\n\n", savePath)
		vcode, promptErr := line.State.Prompt("请输入验证码 > ")
		if promptErr != nil {
			return nil, apierror.NewApiErrorWithError(promptErr)
		}
		wtoken, apiErr = safeWebLoginWithCaptcha(username, password, strings.TrimSpace(vcode))
		if apiErr == nil {
			return wtoken, nil
		}
		fmt.Printf("验证码登录失败 (错误代码: %d，错误信息: %s)\n", apiErr.Code, apiErr.Error())
	}
	return nil, apierror.NewFailedApiError("验证码重试次数过多")
}

func safeWebLogin(username, password string) (wtoken *cloudpan.WebLoginToken, apiErr *apierror.ApiError) {
	defer func() {
		if r := recover(); r != nil {
			apiErr = apierror.NewFailedApiError(fmt.Sprintf("网页登录时发生 panic: %v", r))
		}
	}()
	return cloudpan.Login(username, password)
}

func safeWebLoginWithCaptcha(username, password, captcha string) (wtoken *cloudpan.WebLoginToken, apiErr *apierror.ApiError) {
	defer func() {
		if r := recover(); r != nil {
			apiErr = apierror.NewFailedApiError(fmt.Sprintf("验证码登录时发生 panic: %v", r))
		}
	}()
	return cloudpan.LoginWithCaptcha(username, password, captcha)
}

func TryLogin() *config.PanUser {
	// can do automatically login?
	for _, u := range config.Config.UserList {
		if u.UID == config.Config.ActiveUID {
			// login
			_, _, webToken, appToken, err := DoLoginHelper(config.DecryptString(u.LoginUserName), config.DecryptString(u.LoginUserPassword))
			if err != nil {
				logger.Verboseln("automatically login error: ", err)
				break
			}
			// success
			u.WebToken = webToken
			u.AppToken = appToken

			// save
			_ = SaveConfigFunc(nil)
			// reload
			_ = ReloadConfigFunc(nil)
			return config.Config.ActiveUser()
		}
	}
	return nil
}

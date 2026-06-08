package command

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/tickstep/cloudpan189-api/cloudpan"
	"github.com/tickstep/cloudpan189-go/cmder"
	"github.com/tickstep/cloudpan189-go/internal/config"
	"github.com/tickstep/cloudpan189-go/internal/functions/qrlogin"
	"github.com/tickstep/library-go/requester"
	"github.com/urfave/cli"
)

func CmdQrLogin() cli.Command {
	return cli.Command{
		Name:  "qrlogin",
		Usage: "扫码登录天翼云盘账号",
		Description: `通过天翼云盘网页登录二维码登录，适合密码登录触发二次验证时使用。

示例:
  cloudpan189-go qrlogin
  cloudpan189-go qrlogin --timeout 180
`,
		Category: "天翼云盘账号",
		Before:   cmder.ReloadConfigFunc,
		After:    cmder.SaveConfigFunc,
		Action: func(c *cli.Context) error {
			timeout := time.Duration(c.Int("timeout")) * time.Second
			result, err := qrlogin.Login(timeout, func(imageURL string) {
				fmt.Println("请用天翼云盘 App 扫描二维码登录：")
				fmt.Println(imageURL)
				if savePath, e := saveQRCodeImage(imageURL); e == nil {
					fmt.Println("二维码图片已保存：", savePath)
				}
			})
			if err != nil {
				fmt.Println("扫码登录失败：", err)
				return err
			}

			loginName := result.UserInfo.LoginName
			if loginName == "" {
				loginName = result.LoginName
			}
			nickname := result.UserInfo.Nickname
			if nickname == "" {
				nickname = loginName
			}
			cloudUser := &config.PanUser{
				UID:                     result.UserInfo.UID,
				Nickname:                nickname,
				AccountName:             loginName,
				Sex:                     "U",
				Workdir:                 "/",
				WorkdirFileEntity:       *cloudpan.NewAppFileEntityForRootDir(),
				FamilyWorkdir:           "/",
				FamilyWorkdirFileEntity: *cloudpan.NewAppFileEntityForRootDir(),
				WebToken:                result.WebToken,
				AppToken:                result.AppToken,
			}
			config.Config.SetActiveUser(cloudUser)
			fmt.Println("天翼帐号扫码登录成功: ", cloudUser.Nickname)
			return nil
		},
		Flags: []cli.Flag{
			cli.IntFlag{Name: "timeout", Usage: "等待扫码确认的超时时间（秒）", Value: 180},
		},
	}
}

func saveQRCodeImage(imageURL string) (string, error) {
	client := requester.NewHTTPClient()
	resp, err := client.Req("GET", imageURL, nil, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("download qr image http status %d", resp.StatusCode)
	}
	savePath := filepath.Join(os.TempDir(), "cloudpan189-go-qrlogin.png")
	f, err := os.OpenFile(savePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return savePath, err
}

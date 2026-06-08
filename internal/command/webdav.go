package command

import (
	"fmt"
	"github.com/tickstep/cloudpan189-go/cmder"
	"github.com/tickstep/cloudpan189-go/internal/config"
	"github.com/tickstep/cloudpan189-go/internal/functions/webdavplus"
	"github.com/urfave/cli"
)

func CmdWebdav() cli.Command {
	return cli.Command{
		Name:  "webdav",
		Usage: "启动 WebDAV 服务",
		Description: `启动本地 WebDAV 服务，供 rclone/curl/系统挂载访问天翼云盘。

示例:
  cloudpan189-go webdav 127.0.0.1:6090
  cloudpan189-go webdav --familyId 0 127.0.0.1:6090

当前 plus 版说明:
  - 支持目录列表、文件读取（含 Range/Seek）、MKCOL 创建目录、PUT 上传/覆盖、DELETE 删除、MOVE 重命名/移动。
  - 可配合 rclone WebDAV 后端做挂载、copy、sync 测试；重要数据建议先用单向增量备份验证。
`,
		Category: "天翼云盘",
		Before:   cmder.ReloadConfigFunc,
		Action: func(c *cli.Context) error {
			if config.Config.ActiveUser() == nil {
				fmt.Println("未登录账号")
				return nil
			}
			if c.NArg() < 1 {
				cli.ShowCommandHelp(c, c.Command.Name)
				return nil
			}
			addr := c.Args().Get(0)
			familyId := parseFamilyId(c)
			fmt.Printf("WebDAV 服务启动: http://%s/ (familyId=%d)\n", addr, familyId)
			return webdavplus.Serve(addr, config.Config.ActiveUser().PanClient(), familyId)
		},
		Flags: []cli.Flag{
			cli.Int64Flag{Name: "familyId", Usage: "家庭云ID，0为个人云"},
		},
	}
}

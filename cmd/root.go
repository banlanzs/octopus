package cmd

import (
	"fmt"
	"os"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   conf.APP_NAME,
	Short: conf.APP_DESC,
}

// doubleClickLaunch 标记本次是否以双击方式启动（未传入任何参数）。
// 双击时默认执行 start 命令，并在退出前暂停，避免错误信息一闪而过。
var doubleClickLaunch bool

func Execute() {
	// 禁用 cobra 的 Windows mousetrap：默认情况下 cobra 检测到进程由
	// explorer.exe（双击）启动时会打印 "This is a command line tool..." 并退出。
	// 本程序支持双击直接启动（等效 start），因此清空提示文本禁用该拦截。
	cobra.MousetrapHelpText = ""
	if len(os.Args) == 1 {
		doubleClickLaunch = true
		os.Args = append(os.Args, "start")
	}
	err := rootCmd.Execute()
	if err != nil {
		exitWithPause(1)
	}
}

// exitWithPause 退出程序；双击启动场景下先等待用户按键，
// 让控制台窗口保留在屏幕上，用户能看到启动失败的原因。
func exitWithPause(code int) {
	if doubleClickLaunch {
		fmt.Println("\n按回车键退出...")
		_, _ = fmt.Scanln()
	}
	os.Exit(code)
}

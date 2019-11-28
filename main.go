package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mattn/go-isatty"

	"github.com/fatih/color"
	"go.uber.org/zap/zapcore"

	"go.uber.org/zap"
)

var (
	verbose = flag.Bool("v", false, "Toggle verbose output")
	debug   = flag.Bool("d", false, "enable even more verbose debug output (implies -v)")
	pid     = flag.Int("p", -1, "Select the process ID of FFXIV instead of auto detection")

	l *zap.Logger
)

var (
	neededEnv = []string{
		"DRI_PRIME",
		"LD_LIBRARY_PATH",
		"PYTHONPATH",
		"TERM",
		"PATH",
		"WINE",
		"SHELL",

		"SteamUser",
		"SteamGameId",
		"SteamAppId",
		"SteamClientLaunch",
		"SteamAppUser",

		"EnableConfiguratorSupport",
		"ENABLE_VK_LAYER_VALVE_steam_overlay_1",
		"DXVK",
		"DXVK_LOG_LEVEL",

		"STEAM_ZENITY",
		"STEAM_RUNTIME",
		"STEAM_RUNTIME_LIBRARY_PATH",
		"STEAM_CLIENT_CONFIG_FILE",
		"STEAM_COMPAT_CLIENT_INSTALL_PATH",
		"STEAM_COMPAT_DATA_PATH",
		"STEAMSCRIPT_VERSION",

		"SDL_VIDEO_X11_DGAMOUSE",
		"SDL_GAMECONTROLLERCONFIG",
		"SDL_GAMECONTROLLER_IGNORE_DEVICES",
		"SDL_GAMECONTROLLER_ALLOW_STEAM_VIRTUAL_GAMEPAD",
		"SDL_VIDEO_FULLSCREEN_DISPLAY",

		"SteamStreamingHardwareEncodingNVIDIA",
		"SteamStreamingHardwareEncodingIntel",
		"SteamStreamingHardwareEncodingAMD",

		"WINEDEBUG",
		"WINEDLLPATH",
		"WINEPREFIX",
		"WINE_MONO_OVERRIDES",
		"WINEESYNC",
		"WINEDLLOVERRIDES",
		"WINELOADERNOEXEC",
		"WINEPRELOADRESERVE",
		"PROTON_VR_RUNTIME",
	}
)

func inEnv(key string) bool {
	for _, k := range neededEnv {
		if key == k {
			return true
		}
	}
	return false
}

func isSteam(env map[string]string) bool {
	_, ok := env["SteamUser"]
	return ok
}

func init() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage of %s:\n", os.Args[0])
		flag.PrintDefaults()
		println()
		println("  [exec [args...]]")
		println("        Provide a custom application to run.")
		println()
		println("Redirect the command output to create a shell script instead of dropping a shell or launching the specified command")
	}

	flag.Parse()
	l = logger()
}

func logger() *zap.Logger {
	enccfg := zapcore.EncoderConfig{
		MessageKey:     "msg",
		LevelKey:       "level",
		TimeKey:        "time",
		NameKey:        "name",
		CallerKey:      "caller",
		StacktraceKey:  "stack",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalColorLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
		EncodeDuration: zapcore.NanosDurationEncoder,
		EncodeName:     zapcore.FullNameEncoder,
	}

	enc := zapcore.NewConsoleEncoder(enccfg)
	var out io.Writer = color.Error

	min := zapcore.WarnLevel
	if *verbose {
		min = zapcore.InfoLevel
	}
	if *debug {
		min = zapcore.DebugLevel
	}
	core := zapcore.NewCore(enc, zapcore.AddSync(out), logLevelBetween(min, zapcore.FatalLevel))

	var options []zap.Option
	if *debug {
		options = []zap.Option{
			zap.AddCaller(),
			zap.AddStacktrace(logLevelBetween(zapcore.WarnLevel, 0)),
			zap.Development(),
		}
	}
	return zap.New(core, options...)
}

func logLevelBetween(min, max zapcore.Level) zap.LevelEnablerFunc {
	return zap.LevelEnablerFunc(func(l zapcore.Level) bool {
		return l >= min && l <= max
	})
}

func detectFFXIVPID() (int, error) {
	if *pid > -1 {
		l.Info("using provided PID", zap.Int("pid", *pid))
		return *pid, nil
	}

	matches, err := filepath.Glob("/proc/[0-9]*")
	if err != nil {
		return -1, err
	}
	l.Debug("found process list", zap.Int("count", len(matches)))
	detected := -1

	for _, match := range matches {
		buff, err := ioutil.ReadFile(match + "/cmdline")
		if err != nil {
			l.Warn("failed to read cmdline", zap.String("path", match))
			continue
		}

		cmdline := strings.Split(string(buff), string([]byte{0}))
		if len(cmdline) < 2 || !strings.Contains(cmdline[1], ".exe") {
			continue
		}

		if strings.Contains(cmdline[1], "ffxivboot.exe") {
			detectedStr := strings.TrimSpace(match[6:])
			detected, _ = strconv.Atoi(detectedStr)
			break
		}
	}

	if detected > 0 {
		return detected, nil
	}

	return -1, errors.New("process not found")
}

func environ(pid int) (map[string]string, error) {
	buff, err := ioutil.ReadFile(fmt.Sprintf("/proc/%v/environ", pid))
	if err != nil {
		return nil, err
	}
	environ := strings.Split(string(buff), string([]byte{0}))
	emap := make(map[string]string)
	for _, ev := range environ {
		evp := strings.SplitN(ev, "=", 2)
		if len(evp) < 2 {
			if evp[0] == "WINE" {
				emap["WINE"] = ""
			}
			continue
		}

		if inEnv(evp[0]) {
			emap[evp[0]] = evp[1]
		}
	}

	_, ok := emap["TERM"]
	if !ok {
		emap["TERM"] = "xterm"
	}

	_, ok = emap["SHELL"]
	if !ok {
		emap["SHELL"] = "/bin/bash"
	}

	return emap, nil
}

func fixPath(env map[string]string) map[string]string {
	path, _ := env["PATH"]
	elements := strings.Split(path, ":")
	for i := len(elements) - 1; i <= 0; i-- {
		if !strings.Contains(strings.ToLower(elements[i]), "steam") {
			elements = append(elements[:i], elements[i+1:]...)
		}
	}

	env["PATH"] = strings.Replace(
		strings.Join(elements, ":")+":"+filepath.Dir(env["WINE"])+":"+os.Getenv("PATH"),
		":.:", // dont keep . in path, sideeffect of filepath.Dir with an empty string
		":",
		-1,
	)
	return env
}

func shellArgs(args []string) []string {
	nArgs := make([]string, len(args))

	for i, arg := range args {
		nArgs[i] = fmt.Sprintf(`"%v"`, strings.Replace(arg, "\"", "\\\"", -1))
	}

	return nArgs
}

func main() {
	l.Debug("finding ffxiv pid")
	pid, err := detectFFXIVPID()
	if err != nil {
		l.Fatal("failed to find ffxiv!", zap.Error(err))
	}
	l.Info("selected pid", zap.Int("pid", pid))

	l.Debug("build environment")
	env, err := environ(pid)
	if err != nil {
		l.Fatal("failed to fetch environ!", zap.Error(err))
	}
	env = fixPath(env)

	args := flag.Args()

	l.Debug("check for pipe")
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		l.Info("building shell script")

		fmt.Println("#!/bin/sh")
		fmt.Println()
		for key, val := range env {
			fmt.Printf("export %v=\"%v\"\n", key, strings.Replace(val, "\"", "\\\"", -1))
		}
		fmt.Println("\ncd $WINEPREFIX/drive_c")
		fmt.Fprintln(os.Stderr, os.Stdout.Fd())
		if len(args) == 0 {
			fmt.Println("$SHELL")
		} else {
			fmt.Printf("%v %v\n", args[0], strings.Join(shellArgs(args[1:]), " "))
		}
	} else {
		l.Info("launching in environment")

		cwd, _ := os.Getwd()
		os.Chdir(filepath.Join(env["WINEPREFIX"], "drive_c"))

		var cmd *exec.Cmd
		if len(args) == 0 {
			cmd = exec.Command(env["SHELL"])
		} else {
			cmd = exec.Command(args[0], args[1:]...)
		}

		for key, val := range env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%v=\"%v\"", key, strings.Replace(val, "\"", "\\\"", -1)))
		}

		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		cmd.Run()

		os.Chdir(cwd)
	}
}

package main

// "qs-podscript connect": add a shared server from the command line - the
// same as Setup -> "Where you work" -> "Add a server". Used by the
// installers (install.sh --connect, install.ps1 -Connect). The password
// comes from QSPODSCRIPT_PASSWORD (installers) or is asked for without
// showing it; it is only used to log in once - the server gives this
// computer its own key.

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const connectUsage = `usage: qs-podscript connect <server address> --user NAME [--label TEXT] [--work]
       qs-podscript connect list
  --user NAME    your login on the server
  --label TEXT   name for the server on this computer (optional)
  --work         tick "Transcribe for this server" (this computer then helps
                 when you press "Start transcribing")
  The password is asked for (not shown), or taken from QSPODSCRIPT_PASSWORD.`

func cmdConnect(args []string) error {
	if len(args) > 0 && args[0] == "list" {
		st, err := openStore()
		if err != nil {
			return err
		}
		defer st.Close()
		list := savedServers(st)
		if len(list) == 0 {
			fmt.Println("No servers saved.")
		}
		for _, c := range list {
			work := ""
			if c.Work {
				work = "  (transcribes for it)"
			}
			fmt.Printf("%-28s %-40s as %s%s\n", c.Label(), c.URL, c.User, work)
		}
		return nil
	}
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	user := fs.String("user", "", "your login on the server")
	label := fs.String("label", "", "name for the server on this computer")
	work := fs.Bool("work", false, "transcribe for this server")
	rest := parseInterleaved(fs, args)
	if len(rest) != 1 || strings.TrimSpace(*user) == "" {
		return errors.New(connectUsage)
	}
	pw := os.Getenv("QSPODSCRIPT_PASSWORD")
	if pw == "" {
		var err error
		if pw, err = askHidden("Password for " + *user + ": "); err != nil {
			return err
		}
	}
	if pw == "" {
		return errors.New("no password given")
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, err := connectRemote(ctx, st, rest[0], strings.TrimSpace(*user), pw)
	if err != nil {
		return fmt.Errorf("could not connect: %w", err)
	}
	updateServer(st, id, func(c *remoteConf) {
		if l := strings.TrimSpace(*label); l != "" {
			c.Name = limitLen(l, 60)
		}
		if *work {
			c.Work = true
		}
	})
	c, _ := serverByID(st, id)
	logf("Connected to %s as %s (command line)", c.URL, c.User)
	fmt.Printf("Connected to %s as %s.\n", c.Label(), c.User)
	if c.Work {
		fmt.Println("\"Start transcribing\" continues with this server's episodes after your own.")
	}
	fmt.Println("Switch to it in QS-PodScript with the menu next to the name at the top (restart QS-PodScript if it is running).")
	return nil
}

// askHidden reads a line from the terminal without showing it (stty on
// Linux/macOS; on Windows the installer asks instead and passes it on).
func askHidden(prompt string) (string, error) {
	fi, _ := os.Stdin.Stat()
	tty := fi != nil && fi.Mode()&os.ModeCharDevice != 0
	if tty {
		fmt.Fprint(os.Stderr, prompt)
		if runtime.GOOS != "windows" {
			if sttyEcho(false) == nil {
				defer func() { sttyEcho(true); fmt.Fprintln(os.Stderr) }()
			}
		}
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("no password given")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func sttyEcho(on bool) error {
	arg := "-echo"
	if on {
		arg = "echo"
	}
	cmd := exec.Command("stty", arg)
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

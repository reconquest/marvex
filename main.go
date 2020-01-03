package main

import (
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/docopt/docopt-go"
	"github.com/mattn/go-shellwords"
	"github.com/nsf/termbox-go"
	"github.com/reconquest/executil-go"
	"github.com/reconquest/karma-go"
	"github.com/seletskiy/i3ipc"
)

const usage = `Marvex 9.0

Usage:
    marvex [options]

Options:
  -e <cmd>                Execute specified command in new terminal.
  -b <path>               Specify path to terminal binary
                           [default: /usr/bin/urxvt].
  -t <tpl>                Specify window title template
                           [default: marvex-%w-%n].
  -c                      Send CTRL-L after opening terminal.
  -s                      Smart split.
  -d                      Dummy mode will start terminal with placeholder,
                           which can be converted into shell by pressing
                           Enter. Pressing CTRL-S or Escape will close
                           placeholder without spawning shell.
  --quiet                 Quiet mode, do not show new terminal name.
  --clear-re <re>         CTRL-L will be send only if following regexp matches
                           current command name [default: ^\w+sh$].
  --class <class>         Set X window class name.
  -r --reserving <count>  Specify count of reserving terminals. [default: 2]
  --lock <file>           Lock file path to prevent assigning same terminal to
                           several urxvt.
                           [default: /var/run/user/$UID/marvex.lock]
  --terminal <template>   Template for new terminal command.
                           [default: @path -name "@class" -title "@title" -e "@command"]
  --tmux-socket <name>    Specify name of tmux socket.
  -v --verbose            Be verbose.
`

type Terminal struct {
	Workspace string
	Number    int
}

var verbose bool

// TODO: add verbose logging, rework error handling (hierarchical erors)
// TODO: separate files by routines: i3/tmux/syscall-wrappers.
func main() {
	rand.Seed(time.Now().UnixNano())

	uid := os.Getuid()

	usage := strings.Replace(usage, "$UID", fmt.Sprint(uid), -1)

	args, _ := docopt.Parse(usage, nil, true, "4.0", false)

	var (
		terminal               = args["-b"].(string)
		terminalTemplate       = args["--terminal"].(string)
		titleTemplate          = args["-t"].(string)
		cmdline, shouldExecute = args["-e"].(string)
		smartSplit             = args["-s"].(bool)
		className, _           = args["--class"].(string)
		shouldClearScreen      = args["-c"].(bool)
		reserving, _           = strconv.Atoi(args["--reserving"].(string))
		lockFile               = args["--lock"].(string)
		dummyMode              = args["-d"].(bool)
		quiet                  = args["--quiet"].(bool)
		tmuxSocket, _          = args["--tmux-socket"].(string)
	)

	verbose = args["--verbose"].(bool)

	i3, err := i3ipc.GetIPCSocket()
	if err != nil {
		log.Fatal(err)
	}

	defer i3.Close()

	var unlock func()

	if !dummyMode || os.Getenv("MARVEX_DUMMY_SESSION") == "" {
		unlock, err = obtainLock(lockFile)
		if err != nil {
			log.Fatal(err)
		}
	}

	workspace, err := getFocusedWorkspace(i3)
	if err != nil {
		log.Fatal(err)
	}

	var (
		terminalID   = newTerminalID()
		terminalName = newTerminalName(
			titleTemplate, workspace.Name, terminalID,
		)

		terminalSession = getTerminalSession(
			workspace.Name, terminalID,
		)
	)

	if dummyMode {
		if os.Getenv("MARVEX_DUMMY_SESSION") == "" {
			unlock()

			err := runTerminal(
				i3,
				terminal,
				terminalTemplate,
				terminalName,
				className,
				[]string{os.Args[0], "-d"},
				smartSplit,
				append(os.Environ(), "MARVEX_DUMMY_SESSION="+terminalSession),
			)
			if err != nil {
				log.Fatal(err)
			}

			return
		} else {
			terminalSession = os.Getenv("MARVEX_DUMMY_SESSION")

			err := termbox.Init()
			if err != nil {
				log.Fatal(err)
			}

		loop:
			for {
				switch ev := termbox.PollEvent(); ev.Type {
				case termbox.EventKey:
					switch ev.Key {
					case termbox.KeyEnter:
						termbox.Close()
						break loop

					case termbox.KeyEsc, termbox.KeyCtrlC:
						termbox.Close()
						return
					}
				}
			}
		}
	}

	err = makeTmuxSession(tmuxSocket, terminalSession)
	if err != nil {
		log.Fatal(err)
	}

	if dummyMode {
		err = syscall.Exec(
			"/usr/bin/tmux",
			getTmuxAttachCommand(tmuxSocket, terminalSession),
			os.Environ(),
		)

		log.Fatal(err)

		return // should not be reachable
	} else {
		err = runTerminal(
			i3,
			terminal,
			terminalTemplate,
			terminalName,
			className,
			getTmuxAttachCommand(tmuxSocket, terminalSession),
			smartSplit,
			os.Environ(),
		)
		if err != nil {
			log.Fatal(err)
		}

		if shouldClearScreen {
			err := clearScreen(args["--clear-re"].(string), terminalSession)
			if err != nil {
				log.Fatal(err)
			}
		}
	}

	if shouldExecute {
		err := tmuxSend(tmuxSocket, terminalSession, cmdline)
		if err != nil {
			log.Fatal(err)
		}
	}

	if !quiet {
		fmt.Println(terminalSession)
	}

	err = reserveTerminals(tmuxSocket, reserving)
	if err != nil {
		log.Fatal(err)
	}
}

func getTmuxAttachCommand(socket string, session string) []string {
	args := []string{"/usr/bin/tmux"}
	if socket != "" {
		args = append(args, "-L", socket)
	}
	args = append(args, "attach", "-t", session)
	return args
}

func obtainLock(lockFilePath string) (func(), error) {
	handle, err := os.OpenFile(lockFilePath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf(
			"can't open lock file '%s': %s",
			lockFilePath,
			err,
		)
	}

	err = syscall.Flock(int(handle.Fd()), syscall.LOCK_EX)
	if err != nil {
		return nil, fmt.Errorf(
			"can't lock opened lock file '%s': %s",
			lockFilePath,
			err,
		)
	}

	return func() {
		handle.Close()
	}, nil
}

func reserveTerminals(socket string, need int) error {
	reserved := 0

	sessions := tmuxListSessions(socket)
	for _, session := range sessions {
		if strings.HasPrefix(session, "marvex-reserve-") {
			reserved++
		}
	}

	for i := 0; i < need-reserved; i++ {
		var args []string
		if socket != "" {
			args = append(args, "-L", socket)
		}

		args = append(
			args,
			"new-session",
			"-d",
			"-s",
			fmt.Sprintf(
				"marvex-reserve-%d",
				time.Now().UnixNano(),
			),
		)

		_, _, err := executil.Run(exec.Command("tmux", args...))
		if err != nil {
			return err
		}
	}

	return nil
}

func makeTmuxSession(socket string, name string) error {
	sessions := tmuxListSessions(socket)
	for _, session := range sessions {
		if strings.HasPrefix(session, "marvex-reserve-") {
			return tmuxRenameSession(socket, session, name)
		}
	}

	return tmuxNewSession(socket, name)
}

func tmuxRenameSession(socket string, old, new string) error {
	var args []string
	if socket != "" {
		args = append(args, "-L", socket)
	}

	args = append(args, "rename-session", "-t", old, new)

	if verbose {
		log.Printf("%v", append([]string{"tmux"}, args...))
	}

	_, _, err := executil.Run(exec.Command("tmux", args...))
	if err != nil {
		if strings.Contains(err.Error(), "no current client") {
			return nil
		}

		return err
	}

	return nil
}

func tmuxNewSession(socket string, name string) error {
	var args []string
	if socket != "" {
		args = append(args, "-L", socket)
	}

	args = append(args, "new-session", "-d", "-s", name)

	if verbose {
		log.Printf("%v", append([]string{"tmux"}, args...))
	}
	_, _, err := executil.Run(exec.Command("tmux", args...))

	return err
}

func tmuxSend(socket string, session, cmdline string) error {
	for !tmuxSessionExists(socket, session) {
		time.Sleep(time.Millisecond * 50)
	}

	var args []string
	if socket != "" {
		args = append(args, "-L", socket)
	}

	args = append(args, "send", "-t", session, cmdline+"\n")

	if verbose {
		log.Printf("%v", append([]string{"tmux"}, args...))
	}
	cmd := exec.Command("tmux", args...)
	_, err := cmd.CombinedOutput()
	return err
}

func splitWindow(i3 *i3ipc.IPCSocket, window i3ipc.I3Node) error {
	var (
		parentLayout = window.Layout
		width        = float32(window.Rect.Width)
		height       = float32(window.Rect.Height)
	)

	if width*float32(0.75) > height && parentLayout != "splith" {
		_, err := i3.Command("split horizontal")
		return err
	}

	if height*float32(0.75) < width && parentLayout != "splitv" {
		_, err := i3.Command("split vertical")
		return err
	}

	return nil
}

func splitWindowModeSmart(i3 *i3ipc.IPCSocket) error {
	window, err := getFocusedWindow(i3)
	if err != nil {
		return err
	}

	var (
		parentLayout = window.Layout
		width        = float32(window.Rect.Width)
		height       = float32(window.Rect.Height)
	)

	if width*float32(0.75) > height && parentLayout != "splith" {
		_, err := i3.Command("split horizontal")
		return err
	}

	if height*float32(0.75) < width && parentLayout != "splitv" {
		_, err := i3.Command("split vertical")
		return err
	}

	return nil
}

func tmuxListSessions(socket string) []string {
	var args []string
	if socket != "" {
		args = append(args, "-L", socket)
	}

	args = append(args)

	args = append(args, "list-sessions", "-F", "#S")

	if verbose {
		log.Printf("%v", append([]string{"tmux"}, args...))
	}
	cmd := exec.Command("tmux", args...)
	output, _ := cmd.Output()
	return strings.Split(string(output), "\n")
}

func tmuxSessionExists(socket string, sessionName string) bool {
	for _, tmuxSession := range tmuxListSessions(socket) {
		if tmuxSession == sessionName {
			return true
		}
	}

	return false
}

func getTerminalSession(workspace string, id string) string {
	return fmt.Sprintf("marvex-%s-%s", workspace, id)
}

func newTerminalID() string {
	const consonants = "bcdfghjklmnpqrstvwxz"
	const vowels = "aeiouy"

	count := 5
	result := ""
	for i := 0; i < count; i++ {
		result += string(consonants[rand.Intn(len(consonants))])
		result += string(vowels[rand.Intn(len(vowels))])
	}
	return result
}

func newTerminalName(
	template string,
	workspace string,
	id string,
) string {
	result := strings.Replace(template, "%w", workspace, -1)
	result = strings.Replace(result, "%n", id, -1)

	return result
}

func runTerminal(
	i3 *i3ipc.IPCSocket,
	path string,
	cmdTemplate string,
	title string,
	class string,
	command []string,
	smartSplit bool,
	env []string,
) error {
	if smartSplit {
		err := splitWindowModeSmart(i3)
		if err != nil {
			return err
		}
	}

	for index, envValue := range env {
		if strings.HasPrefix(envValue, "TMUX=") {
			env[index] = ""
			break
		}
	}

	if !filepath.IsAbs(path) {
		absPath, err := exec.LookPath(path)
		if err != nil {
			return karma.Format(
				err,
				"unable to lookup %s", path,
			)
		}

		path = absPath
	}

	replacer := strings.NewReplacer(
		"@title", title,
		"@class", class,
		"@path", path,
		`"@path"`, strings.Join(command, " "),
	)

	args, err := shellwords.Parse(cmdTemplate)
	if err != nil {
		return karma.Format(
			err,
			"unable to parse command line template",
		)
	}

	if verbose {
		log.Printf("%q", args)
	}

	for index, arg := range args {
		args[index] = replacer.Replace(arg)
	}

	if args[len(args)-1] == "@command" {
		args = append(args[:len(args)-1], command...)
	}

	if verbose {
		log.Printf("%q", args)
	}

	_, err = syscall.ForkExec(
		path,
		args,
		&syscall.ProcAttr{
			Env: env,
			Files: []uintptr{
				uintptr(syscall.Stdin),
				uintptr(syscall.Stdout),
				uintptr(syscall.Stderr),
			},
		},
	)

	return err
}

func getFocusedWindow(i3 *i3ipc.IPCSocket) (i3ipc.I3Node, error) {
	tree, err := i3.GetTree()
	if err != nil {
		return tree, err
	}

	var walker func(i3ipc.I3Node) (i3ipc.I3Node, bool)

	walker = func(node i3ipc.I3Node) (i3ipc.I3Node, bool) {
		for _, subnode := range node.Nodes {
			if subnode.Focused {
				subnode.Layout = node.Layout
				return subnode, true
			}

			activeNode, ok := walker(subnode)
			if ok {
				return activeNode, true
			}
		}

		return node, false
	}

	node, _ := walker(tree)

	return node, nil
}

func getBiggestNode(node i3ipc.I3Node) (i3ipc.I3Node, int64) {
	var bigNode i3ipc.I3Node
	var bigArea int64

	for _, subnode := range node.Nodes {
		if subnode.Name != "" {
			area := int64(subnode.Rect.Height) * int64(subnode.Rect.Width)

			log.Printf(
				"node: %d | %q | %d x %d = %d",
				subnode.Id,
				subnode.Name,
				subnode.Rect.Height,
				subnode.Rect.Width,
				area,
			)

			if area > bigArea {
				bigNode = subnode
				bigArea = area
			}
		}

		subBigNode, subBigArea := getBiggestNode(subnode)
		if subBigArea > bigArea {
			bigNode = subBigNode
			bigArea = subBigArea
		}
	}

	return bigNode, bigArea
}

func getFocusedWorkspace(i3 *i3ipc.IPCSocket) (i3ipc.Workspace, error) {
	workspaces, err := i3.GetWorkspaces()
	if err != nil {
		return i3ipc.Workspace{}, err
	}

	for _, workspace := range workspaces {
		if workspace.Focused {
			return workspace, nil
		}
	}

	return i3ipc.Workspace{}, fmt.Errorf("could not found focused workspace")
}

func getActiveTerminals(
	template string,
	tree i3ipc.I3Node,
	workspace i3ipc.Workspace,
) ([]Terminal, error) {
	outputNode, err := getOutputNode(tree.Nodes, workspace.Output)
	if err != nil {
		return []Terminal{}, err
	}

	contentNode, err := getContentNode(outputNode.Nodes)
	if err != nil {
		return []Terminal{}, err
	}

	workspaceNode, err := getWorkspaceNode(contentNode.Nodes, workspace.Name)
	if err != nil {
		return []Terminal{}, err
	}

	reTemplateBody := template
	reTemplateBody = strings.Replace(reTemplateBody, "%w", "([0-9a-z]+)", -1)
	reTemplateBody = strings.Replace(reTemplateBody, "%n", "([0-9a-z]+)", -1)

	reTemplate, err := regexp.Compile(reTemplateBody)
	if err != nil {
		log.Fatal(err)
	}

	terminals := recursiveSearchTerminals(workspaceNode.Nodes, reTemplate)

	return terminals, err
}

func recursiveSearchTerminals(
	nodes []i3ipc.I3Node,
	reName *regexp.Regexp,
) []Terminal {
	terminals := []Terminal{}
	for _, node := range nodes {
		matches := reName.FindStringSubmatch(node.Name)
		if len(matches) > 0 {
			number, _ := strconv.Atoi(matches[2])
			terminal := Terminal{
				Workspace: matches[1],
				Number:    number,
			}
			terminals = append(terminals, terminal)
			continue
		}

		if len(node.Nodes) > 0 {
			terminals = append(
				terminals,
				recursiveSearchTerminals(node.Nodes, reName)...,
			)
		}
	}

	return terminals
}

func getContentNode(outputNodes []i3ipc.I3Node) (i3ipc.I3Node, error) {
	for _, contentNode := range outputNodes {
		if contentNode.Name == "content" {
			return contentNode, nil
		}
	}

	return i3ipc.I3Node{}, fmt.Errorf(
		"could not find content node of workspace output root node",
	)
}

func getOutputNode(
	rootNodes []i3ipc.I3Node,
	output string,
) (i3ipc.I3Node, error) {
	for _, outputNode := range rootNodes {
		if outputNode.Name == output {
			return outputNode, nil
		}
	}

	return i3ipc.I3Node{}, fmt.Errorf(
		"could not find root node of workspace output: "+
			"output = %s, rootNodes = %#v",
		output, rootNodes,
	)
}

func getWorkspaceNode(
	workspaceNodes []i3ipc.I3Node, workspaceName string,
) (i3ipc.I3Node, error) {
	for _, workspaceNode := range workspaceNodes {
		if workspaceNode.Name == workspaceName {
			return workspaceNode, nil
		}
	}

	return i3ipc.I3Node{}, fmt.Errorf(
		"could not find workspace node: "+
			"workspaceName = %s, wokspaceNodes = %#v",
		workspaceName, workspaceNodes,
	)
}

func clearScreen(matchRegexp, sessionName string) error {
	attached, commandName := waitSessionToAttach(sessionName)

	isShell, _ := regexp.MatchString(matchRegexp, commandName)
	if !attached || !isShell {
		return nil
	}

	args := []string{"send-keys", "-R", "-t", sessionName, "C-l"}

	if verbose {
		log.Printf("%v", append([]string{"tmux"}, args...))
	}

	cmd := exec.Command("tmux", args...)
	_, err := cmd.CombinedOutput()

	return err
}

func waitSessionToAttach(sessionName string) (bool, string) {
	// tmux totally can't into the space.
	//
	// Even after session is marked as attached in the session list, it doesn't
	// mean that it's fully initialized, and there is no way to find when tmux
	// is initialized.
	//
	// When session is not attached, tmux reports window size as 80x24.
	//
	// And we can't clear the screen until tmux fix window position according
	// to the parent size.
	probablyNotInitialized := true

	for {
		cmd := exec.Command(
			"tmux", "list-sessions", "-F",
			"#S:#{?session_attached,X,}:"+
				"#{window_width}x#{window_height}:#{pane_current_command}",
		)

		output, err := cmd.Output()
		if err != nil {
			return false, ""
		}

		tmuxSessions := strings.Split(string(output), "\n")
		for _, tmuxSession := range tmuxSessions {
			if strings.HasPrefix(tmuxSession, sessionName+":X") {
				geometryAndCommand := strings.SplitN(
					tmuxSession[len(sessionName)+3:], ":", 2,
				)

				geometry := geometryAndCommand[0]

				// It's called probablyNotInitialized because we actually can
				// have window with size of 80x24
				if geometry == "80x24" && probablyNotInitialized {
					probablyNotInitialized = false
					continue
				}

				commandName := geometryAndCommand[1]

				return true, commandName
			}
		}
	}
}

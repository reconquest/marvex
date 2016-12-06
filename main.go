package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/docopt/docopt-go"
	"github.com/proxypoke/i3ipc"
	"github.com/reconquest/executil-go"
)

const usage = `Marvex 3.0

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
  --clear-re <re>         CTRL-L will be send only if following regexp matches
                           current command name [default: ^\w+sh$].
  --class <class>         Set X window class name.
  -r --reserving <count>  Specify count of reserving terminals. [default: 2]
  --lock <file>           Lock file path to prevent assigning same terminal to
                            several urxvt.
                            [default: /var/run/user/$UID/marvex.lock]
`

type Terminal struct {
	Workspace string
	Number    int
}

// TODO: add verbose logging, rework error handling (hierarchical erors)
func main() {
	uid := os.Getuid()

	usage := strings.Replace(usage, "$UID", fmt.Sprint(uid), -1)

	args, _ := docopt.Parse(usage, nil, true, "3.0", false)

	var (
		terminalPath           = args["-b"].(string)
		titleTemplate          = args["-t"].(string)
		cmdline, shouldExecute = args["-e"].(string)
		smartSplit             = args["-s"].(bool)
		className, _           = args["--class"].(string)
		shouldClearScreen      = args["-c"].(bool)
		reserving, _           = strconv.Atoi(args["--reserving"].(string))
		lockFile               = args["--lock"].(string)
	)

	err := obtainLock(lockFile)
	if err != nil {
		log.Fatal(err)
	}

	i3, err := i3ipc.GetIPCSocket()
	if err != nil {
		log.Fatal(err)
	}

	defer i3.Close()

	tree, err := i3.GetTree()
	if err != nil {
		log.Fatal(err)
	}

	workspace, err := getFocusedWorkspace(i3)
	if err != nil {
		log.Fatal(err)
	}

	terminals, err := getActiveTerminals(
		titleTemplate,
		tree,
		workspace,
	)
	if err != nil {
		log.Fatal(err)
	}

	var (
		terminalNumber = getNewTerminalNumber(terminals)
		terminalName   = getTerminalName(
			titleTemplate, workspace.Name, terminalNumber,
		)

		terminalSession = getTerminalSession(
			workspace.Name, terminalNumber,
		)
	)

	if !tmuxSessionExists(terminalSession) {
		err := makeTmuxSession(terminalSession)
		if err != nil {
			log.Fatal(err)
		}
	}

	if smartSplit {
		err = splitWorkspace(i3)
		if err != nil {
			log.Fatal(err)
		}
	}

	err = runTerminal(
		terminalPath, terminalName, className,
		"tmux attach -t "+terminalSession,
		true,
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

	if shouldExecute {
		err := tmuxSend(terminalSession, cmdline)
		if err != nil {
			log.Fatal(err)
		}
	}

	fmt.Println(terminalSession)

	err = reserveTerminals(reserving)
	if err != nil {
		log.Fatal(err)
	}
}

func obtainLock(lockFilePath string) error {
	handle, err := os.OpenFile(lockFilePath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf(
			"can't open lock file '%s': %s",
			lockFilePath,
			err,
		)
	}

	err = syscall.Flock(int(handle.Fd()), syscall.LOCK_EX)
	if err != nil {
		return fmt.Errorf(
			"can't lock opened lock file '%s': %s",
			lockFilePath,
			err,
		)
	}

	return nil
}

func reserveTerminals(need int) error {
	reserved := 0

	sessions := tmuxListSessions()
	for _, session := range sessions {
		if strings.HasPrefix(session, "marvex-reserve-") {
			reserved++
		}
	}

	for i := 0; i < need-reserved; i++ {
		_, _, err := executil.Run(
			exec.Command(
				"tmux",
				"new-session",
				"-d",
				"-s",
				fmt.Sprintf(
					"marvex-reserve-%d",
					time.Now().UnixNano(),
				),
			),
		)
		if err != nil {
			return err
		}
	}

	return nil
}

func makeTmuxSession(name string) error {
	sessions := tmuxListSessions()
	for _, session := range sessions {
		if strings.HasPrefix(session, "marvex-reserve-") {
			return tmuxRenameSession(session, name)
		}
	}

	return tmuxNewSession(name)
}

func tmuxRenameSession(old, new string) error {
	_, _, err := executil.Run(
		exec.Command("tmux", "rename-session", "-t", old, new),
	)

	return err
}

func tmuxNewSession(name string) error {
	_, _, err := executil.Run(
		exec.Command("tmux", "new-session", "-d", "-s", name),
	)

	return err
}

func tmuxSend(session, cmdline string) error {
	for !tmuxSessionExists(session) {
		time.Sleep(time.Millisecond * 50)
	}

	cmd := exec.Command(
		"tmux", "send", "-t", session, cmdline+"\n",
	)

	_, err := cmd.CombinedOutput()
	return err
}

func splitWorkspace(i3 *i3ipc.IPCSocket) error {
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
		_, err = i3.Command("split horizontal")
		return err
	}

	if height*float32(0.75) < width && parentLayout != "splitv" {
		_, err = i3.Command("split vertical")
		return err
	}

	return nil
}

func tmuxListSessions() []string {
	cmd := exec.Command("tmux", "list-sessions", "-F", "#S")
	output, _ := cmd.Output()
	return strings.Split(string(output), "\n")

}
func tmuxSessionExists(sessionName string) bool {
	for _, tmuxSession := range tmuxListSessions() {
		if tmuxSession == sessionName {
			return true
		}
	}

	return false
}

func getTerminalSession(workspace string, terminalNumber int) string {
	return fmt.Sprintf("marvex-%s-%d", workspace, terminalNumber)
}

func getNewTerminalNumber(terminals []Terminal) int {
	newTerminalNumber := 1
	for {
		found := true
		for _, terminal := range terminals {
			if newTerminalNumber == terminal.Number {
				newTerminalNumber = newTerminalNumber + 1
				found = false
				break
			}
		}

		if found {
			break
		}
	}

	return newTerminalNumber
}

func getTerminalName(
	template string,
	workspace string,
	number int,
) string {
	result := strings.Replace(template, "%w", workspace, -1)
	result = strings.Replace(result, "%n", strconv.Itoa(number), -1)

	return result
}

func runTerminal(
	path string,
	title string,
	class string,
	command string,
	removeEnvTMUX bool,
) error {
	envValues := os.Environ()
	if removeEnvTMUX {
		for index, envValue := range envValues {
			if strings.HasPrefix(envValue, "TMUX=") {
				envValues[index] = ""
				break
			}
		}
	}

	args := []string{
		path, "-title", title,
	}

	if class != "" {
		args = append(args, "-name", class)
	}

	args = append(args, "-e")
	args = append(args, strings.Split(command, " ")...)

	_, err := syscall.ForkExec(
		path,
		args,
		&syscall.ProcAttr{Env: envValues},
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

	cmd := exec.Command("tmux", "send-keys", "-R", "-t", sessionName, "C-l")
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

	return false, ""
}

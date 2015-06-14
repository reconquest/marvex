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

	"github.com/docopt/docopt-go"
	"github.com/proxypoke/i3ipc"
)

const usage = `Marvex 0.1
Usage:
	marvex [options]

Options:
	-b <path>  Specify path to terrminal binary
			   [default: /usr/bin/urxvt].
	-t <tpl>   Specify window title template
			   [default: marvex: %w-%n].
`

type Terminal struct {
	Workspace string
	Number    int
}

func main() {
	args, _ := docopt.Parse(usage, nil, true, "1.0", false)

	var (
		terminalPath  = args["-b"].(string)
		titleTemplate = args["-t"].(string)
	)

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

	err = i3.Close()
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

	newTerminalNumber := getNewTerminalNumber(terminals)
	newTerminalTitle := getNewTerminalTitle(
		titleTemplate, workspace.Name, newTerminalNumber,
	)
	newTerminalSessionName := tmuxGetSessionName(
		workspace.Name, newTerminalNumber,
	)

	var tmuxArguments string
	if tmuxSessionExists(newTerminalSessionName) {
		tmuxArguments = "attach -t " + newTerminalSessionName
	} else {
		tmuxArguments = "new-session -s " + newTerminalSessionName
	}

	tmuxCommand := "tmux " + tmuxArguments

	err = runTerminal(
		terminalPath,
		newTerminalTitle,
		tmuxCommand,
		true,
	)
	if err != nil {
		log.Fatal(err)
	}
}

func tmuxSessionExists(sessionName string) bool {
	cmd := exec.Command("tmux", "list-sessions", "-F", "#S")
	output, _ := cmd.Output()
	tmuxSessions := strings.Split(string(output), "\n")

	for _, tmuxSession := range tmuxSessions {
		if tmuxSession == sessionName {
			return true
		}
	}

	return false
}

func tmuxGetSessionName(workspace string, terminalNumber int) string {
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

func getNewTerminalTitle(template string, workspace string, number int) string {
	return strings.Replace(
		strings.Replace(template,
			"%w",
			workspace,
			-1,
		),
		"%n",
		strconv.Itoa(number),
		-1,
	)
}

func runTerminal(
	path string,
	title string,
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

	args := append(
		[]string{
			path, "-title", title, "-e",
		},
		strings.Split(command, " ")...,
	)

	_, err := syscall.ForkExec(
		path,
		args,
		&syscall.ProcAttr{Env: envValues},
	)

	return err
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

	reTemplate, err := regexp.Compile(
		strings.Replace(
			strings.Replace(
				template,
				"%w",
				"([0-9a-z])",
				-1,
			),
			"%n",
			"([0-9a-z])",
			-1,
		),
	)
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

func getOutputNode(rootNodes []i3ipc.I3Node, output string) (i3ipc.I3Node, error) {
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

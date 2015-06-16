Don't you tired of sooo slooow zsh + oh-my-zsh/prezto start?

WE HAZ ANSWERZ.

marvex
======

It's the drop-in replacement for urxvt call.

Marvex launches new shell in the separate tmux session and tracks workspace
number where shell was launched.

If shell window is closed via i3 kill binding, subsequent call of marvex will
just reopen closed shell.

Marvex will remember workspace that was used for launching shell, and will
reopen old shells on the appropriate workspace.

In other words, shells actually will never die. *Ghost in z shell*.

## Usage

In your `.i3/config`:

```
bindsym $mod+Return exec marvex
bindsym $mod+Shift+Return exec i3-sensible-terminal
```

## Hints
### Reopened shell messed with previous output!

Add `-c` flag to the `exec marvex` command.

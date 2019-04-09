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

## Troubleshooting
### urxvt: "c": unknown or malformed option.

If you see following output when executing `marvex` command:

```
marvex-2-dyvukutufi
urxvt: "c": unknown or malformed option.                       
urxvt: "/usr/bin/tmux": malformed option.
urxvt: "attach": malformed option.
urxvt: "t": unknown or malformed option.
urxvt: "marvex-2-dyvukutufi": malformed option.
rxvt-unicode (urxvt) v9.22 - released: 2016-01-23
options: perl,xft,styles,combining,blink,iso14755,unicode3,enco+kr+zh+zh-ext,fade,transparent,tint,XIM,frills,selectionscrollirsorBlink,pointerBlank,scrollbars=plain+rxvt+NeXT+xterm
```

Try specifying marvex command line arguments explicitly:

```
marvex -b urxvt --terminal '@path --title "@title" -e @command'
```

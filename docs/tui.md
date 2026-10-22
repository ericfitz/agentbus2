# The terminal UI

`agentbus2 tui` is a dashboard for a human: channels, messages, memories, task lists, search, and the health of the bus. It is a port of the agentbus 1.x TUI that runs over the `Bus` interfaces, so it behaves the same against a local SQLite file and against a server.

## Start

```sh
agentbus2 tui [-profile P] [-identity I]
```

`-profile` and `-identity` select the profile and the identity exactly as they do for every other command (`AGENTBUS2_PROFILE` and `AGENTBUS2_IDENTITY`, then `.local/agentbus2.json`, then the profile's `identity`, then the profile's only credential, then the OS login name, the TUI's own last step; a Windows `DOMAIN\name` is cut to `name`, a `/` becomes `-`, and an unknown user leaves the "identity is required" error). There are no other flags: v1's `-config` and `-as` are rejected (exit 2), and so is a positional argument. `-h` prints the flags and exits 0.

In local mode the TUI also runs the maintenance loop, as v1's TUI ran its background loops; when several local processes run it, a lease row in the database lets one tick at a time. A server runs its own maintenance, so server mode does not.

Exit codes: 0 on a clean quit, 1 with `agentbus2: <error>` on stderr when the session is lost or the program fails, 2 for a usage or profile/identity resolution error.

### Terminal

The TUI is built on Bubble Tea v2 (`charm.land/bubbletea/v2` v2.0.10, Bubbles v2.2.1, Lip Gloss v2.0.6). It draws on the alternate screen and quits on `q` or `ctrl+c`. Only `agentbus2 tui` talks to the terminal: once it starts it asks for the background color (OSC 11), which picks the compose line's light or dark colors (dark until the terminal answers), and Bubble Tea asks for its own capabilities. No other command queries the terminal; with Bubble Tea v1 every `agentbus2` process did so at package init and stalled for up to 5 s on a pty that never answered (post-build decision 2; `TestVersionOnSilentPTY` proves the fix).

Colors follow the terminal's color profile: on a terminal without color (`NO_COLOR`, `TERM=dumb`, or output that is not a terminal) tag chips fall back to dim `#tag` words. The TUI turns on Unicode core mode 2027 so Bubble Tea measures emoji the way the layout does; a terminal without that mode ignores it.

Terminal.app draws the `emoji` set's agent gear two cells wide but advances one. Bubble Tea v1 wrote the gear with a cursor-forward escape to cover the gap; Bubble Tea v2's renderer drops that escape, so in Terminal.app a row with the gear is one column short after it. `nerdfont`, `custom` (`icon_map` with `agent` set to a glyph of fixed width) or another terminal avoid it.

### Identity

The TUI opens a session with the host's short name as the hint (the hostname lowercased and cut at the first `.`), so a human running it as identity `eric` on `MacMini.local` shows as `eric/macmini`, and as `eric/laptop` on another host (`eric/macmini-2` for a second TUI on one host). A hostname that is unavailable or fails the name rule (empty, over 128 bytes, a `/` or a control character) falls back to the hint `tui` (`eric/tui`). The status bar says `as eric`, the identity; the health overlay shows the session's display name. Agents can tell the TUI from the identity's agent sessions. The session is closed on quit, which frees the name at once. A TUI that is killed leaves its session lapsed until the server's retention removes it, and the next start is then `eric/macmini-2`; nothing is lost, because a new session catches up from the identity's acked mark.

Subscriptions and the acked mark belong to the identity, not the session. A human who runs the TUI under the identity the agents use shares their subscriptions and acks with them. Use a distinct identity for the human (`-identity eric`). When `Identities` shows other live sessions of the TUI's identity, `agentbus2 tui` prints one line on stderr before the screen opens and repeats it in the health overlay:

```
agentbus2 tui: identity eric has 1 other live session(s); subscriptions and acks are shared with them (pass -identity for a separate name)
```

When nothing else selects an identity, `tui` (and only `tui`) falls back to the OS login name (source `os-user`, v1's `tui_name`). In a server profile the name is used only when it has a credential file; otherwise it is the usual "identity is required" resolution error. Every other command has no such fallback.

## Layout

Three panes: the rail on the left (shown when the terminal is at least 60 columns wide), the messages in the middle, and the compose box at the bottom, with a status bar as the last line (version, `as <identity>`, the administrator's embedding and notice fields, and the key hints; the mode line is in the health overlay only). `tab` and `shift+tab` move between them.

The rail has five sections, each under a heading:

| Section | Holds |
| --- | --- |
| `channels` | ordinary channels |
| `memories` | channels of kind `memory` |
| `tasks` | channels of kind `tasks` |
| `tags` | the tag sets you follow (`t` on the rail), read-only |
| `sessions` | every live session by display name, then the recently idle ones; `↑`/`↓` select a row and the main pane shows its details |

The section of a channel comes from its kind, never from its name. Rows show the full name. `←`/`→` or `enter` on a heading collapses or expands its section; a collapsed heading shows `(count[, N unread])`. Collapsing lasts until the TUI quits. `home` selects the `channels` heading. A `sessions` row is a rail stop. Selecting it replaces the main pane with that session's details: display name, identity (`unknown` for an idle row, whose identity the TUI never learned), session id (live rows only), and `live` or `idle, last seen <time>`. The details come from what the rail already holds, so selecting a row makes no bus call; there is still no inbox view and no messaging from a session row (ADR 0006), and `i`, `enter`, `s` and `d` do nothing on one. When a refresh removes the selected row, the selection moves to the `sessions` heading.

Rail markers:

- `(off)` after a channel name: you are not subscribed to it (`s` toggles).
- `(no access)` in the dim style: the channel's ACL denies you read. `ListChannels` shows every channel, so the rail lists it, but nothing subscribes to it or reads its history, and selecting it shows `no read access to <name>`.

`<` and `>` narrow and widen the rail and save the width as `tui.rail_width` in `config.json`.

## Keys

The full map is the `?` overlay (`internal/tui/help.go`). The main keys:

| Key | Action |
| --- | --- |
| `tab` / `shift+tab` | next / previous pane |
| `i` | compose |
| `enter` | reply to the cursor message, else compose; in a task list, save the draft; on a rail heading, collapse or expand |
| `esc` | leave compose; cancel a task draft; clear the message cursor |
| `↑` `↓` | previous / next in the focused pane |
| `→` `←` | open / hide a message's body, then its replies; on a heading, expand / collapse |
| `pgup` `pgdn` | scroll (`pgup` at the top loads older history) |
| `g` / `G` | oldest / newest |
| `r` | reply to the cursor message |
| `,` / `.` | newer / older version of the cursor memory |
| `c` | new channel: `<name> [memory\|tasks]` |
| `s` | subscribe / unsubscribe the channel |
| `t` | rail: follow a tag set `<tag>[,<tag>...]` (AND); task list: take the cursor task |
| `u` | task list: release your not-started cursor task |
| `a` | task list: assign the cursor task to an identity |
| `space` | task list: cycle the cursor task's state as a draft |
| `d` | delete the channel and its messages (asks first); on a tag set, unfollow it |
| `/` | search; `tab` cycles text / semantic / both |
| `h` | health and config |
| `?` | help |
| `q` | quit |

In compose: `enter` sends (a memory channel creates a memory), `alt+enter` inserts a newline, `↑` recalls the last sent message, `↓` on the draft's last line clears it, `ctrl+u` clears. `ctrl+c` quits from anywhere.

The channel create prompt takes a kind (`memory` or `tasks`) as its second word; anything else, or a third word, is a usage toast. The TUI has no ACL editor: a channel's ACL is set where the channel is created by an agent or the CLI.

### Task lists

A channel of kind `tasks` shows the task tree. A task is editable when it is unassigned or yours. `space` cycles the draft state (not started, in progress, completed) and `enter` saves it; `esc` cancels. `t` takes the cursor task (claim: it becomes yours and in progress), `u` releases it, and `a` assigns it to an identity chosen from the list of identities. The bus's owner rules stay authoritative; a refusal is a toast.

### Search

`/` opens search. `tab` cycles the mode: text, semantic, both. When the semantic search is unavailable the results are text only and say so.

### Health

`h` shows the version, the mode line (`local · <sqlite path>` or `server · <url>`), your identity, display name and session id, the live sessions, the channels by kind, the receive state (last batch, gaps, last error), the administrator block when your identity is an administrator (capacity notices, the embedding backlog and errors, storage), the warnings printed at start, the theme with each resolved value, the icon set, the config path and the effective `tui` settings. A non-administrator never calls the admin operations (`Status`, `Usage`) and sees a line saying so.

`o` opens `config.json` in `$VISUAL`, else `$EDITOR`, else `vi` (`notepad` on Windows), and re-validates the file when the editor exits. Settings apply the next time the TUI starts.

## Settings

The TUI's settings are the top-level `tui` object of `config.json` in the config directory (`$AGENTBUS2_CONFIG_DIR`, else `$XDG_CONFIG_HOME/agentbus2`, else `~/.config/agentbus2`; on Windows `%APPDATA%\agentbus2` in place of the last two), next to `profiles`:

```json
{
  "profiles": {"default": {"mode": "local", "identity": "eric"}},
  "tui": {
    "theme": "night",
    "themes": [{"name": "night", "background": "black", "agent": "brightcyan", "user": "brightyellow"}],
    "icons": "nerdfont",
    "icon_map": {"chat": ""},
    "rail_width": 32
  }
}
```

Where these settings live is a proposal pending Eric (Milestone 8 item 1); v1 kept them at the top level of its own config file.

| Key | Meaning |
| --- | --- |
| `theme` | name of the theme to apply: `""`, `default` or the name of an entry of `themes` |
| `themes` | named color sets; each `name` is non-empty and unique |
| `icons` | `emoji` (default), `nerdfont` or `custom` |
| `icon_map` | icon name to glyph, applied over the chosen set |
| `rail_width` | rail width in columns, 0 to 1000; 0 is the built-in width |

The file is loaded strictly: an unknown `icons` value, a `theme` that names nothing, a duplicate or empty theme name, or a `rail_width` out of range makes the config fail to load, and the command that loaded it exits with the error. Color values and `icon_map` names are checked by the TUI at start; each bad one prints one line on stderr before the screen opens and falls back to the default.

### Theme

A value is one of the sixteen ANSI color names (`black`, `red`, `green`, `yellow`, `blue`, `magenta`, `cyan`, `white`, or a `bright` form such as `brightblack`), an index `0` to `15`, or `default` for the terminal's own color. Matching is case-insensitive. `#RRGGBB` is rejected. A key a theme leaves out takes the built-in default's value. The built-in `default` theme is always available.

| Key | Used for | Default |
| --- | --- | --- |
| `background` | screen background | `default` |
| `text` | message content | `default` |
| `dim` | dividers, help, summaries | `brightblack` |
| `timestamp` | message timestamps | `brightblack` |
| `tag` | tag chips | `brightblack` |
| `agent` | agent names, selected channel, key hints | `cyan` |
| `user` | your own name | `yellow` |
| `memory` | memory channels and memory versions | `magenta` |
| `tasks` | task-list channels | `yellow` |
| `health` | ok states | `green` |
| `warn` | warnings | `yellow` |
| `error` | errors and the delete confirmation | `red` |
| `selection` | selected row background (focused pane) | `blue` |
| `selection_inactive` | selected rail row while another pane has focus | `brightblack` |
| `selection_text` | text of the selected rail row | `black` |
| `unsaved` | a task row whose drafted state is not saved | `yellow` |

### Icons

`icons` picks the set the TUI draws for channels, senders, task status and markers. `emoji` needs no font setup. `nerdfont` uses Nerd Font glyphs. `custom` is `emoji` overridden by `icon_map`; `icon_map` also applies over `nerdfont`. An icon name that `icon_map` does not set keeps its base glyph. An unknown `icons` value, an unknown `icon_map` name or an empty value prints one line on stderr and falls back to the base set for that icon.

| Icon name | Used for | `nerdfont` codepoint |
| --- | --- | --- |
| `chat` | ordinary channels | U+F27A |
| `memory` | memory channels | U+F0C7 |
| `tasks` | task-list channels | U+F0AE |
| `agent` | other senders and sessions | U+EE0D |
| `user` | your own name | U+F007 |
| `idle` | a session not seen live for a while | U+F04B2 |
| `task_pending` | a pending task | U+F096 |
| `task_in_progress` | an in-progress task | U+F152 |
| `task_completed` | a completed task | U+F14A |
| `collapsed` | selected list item; a message with something hidden | U+F0DA |
| `expanded` | an open body or replies | U+F0D7 |
| `error` | toast and overlay error prefix | U+F06A |
| `arrow` | sender to channel | U+F061 |

A JSON string has no `\U` escape, so a glyph beyond the Basic Multilingual Plane such as `idle`'s needs a surrogate pair (`"\udb81\udcb2"` for U+F04B2) or the literal character. The health overlay names the active set on its `icons ·` line.

### Using a Nerd Font

`nerdfont` needs a Nerd Font installed and selected as the terminal's font for the codepoints above; agentbus2 installs no font. Install a Mono variant: the non-Mono build advances two columns for these glyphs where the rail and the task rows assume one.

```sh
brew install --cask font-sauce-code-pro-nerd-font
```

Then point the terminal at it: iTerm2 (Profiles, Text, "Non-ASCII Font"), kitty (`symbol_map` in `kitty.conf` over the Private Use Area ranges), WezTerm (`font_fallback` in `wezterm.lua`), or, in Terminal.app, the profile's own font (its fallback for these codepoints is unreliable).

Three Font Awesome glyphs (`fa-face-sleeping`, `fa-microchip-ai`, `fa-user-robot`) are Pro-only and not in Nerd Fonts; they are reachable only through `icon_map` with the codepoints of your own licensed font. Nerd Fonts and Font Awesome Pro share some Private Use Area codepoints with different glyphs, so check a custom codepoint against your font before relying on it.

## What differs from v1

- No direct messages and no inbox view (spec 1.3, ADR 0006): a session row shows details only, never messages (TUI decision 14).
- A session row is selectable only to show its details (TUI decision 14); there is no session view of messages.
- Kinds, not prefixes: a task list is a channel of kind `tasks`, a memory channel of kind `memory` (ADR 0008). v1's `general/`, `memory/` and `tasks/` prefixes are not read, and names are shown in full.
- Identity comes from `-profile` and `-identity` (Decision 71), not from `tui_name`, `-as` or the OS user.
- Names shown for messages and sessions are the server's (`Message.Display`, `Session.DisplayName`; ADR 0003).
- Task release is its own operation (`u`), and assigning picks from identities (owners are identities).
- The TUI reads from `Watch` and `Receive` with acks (Decision 83) rather than a blocking `Receive`, and has no per-session repo or context.

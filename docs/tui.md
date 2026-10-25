# The terminal UI

`agentbus2 tui` is a dashboard for a human: channels, messages, memories, task lists, search, and the health of the bus. Unlike an agent, it sees memories: agents never receive memory channels (decision 20), and the TUI, the bus observer, is the exception. It is a port of the agentbus 1.x TUI that runs over the `Bus` interfaces, so it behaves the same against a local SQLite file and against a server.

## Start

```sh
agentbus2 tui [-profile P] [-identity I]
```

On a server profile the TUI is a human who signs in through the corporate provider; see "Server profiles and sign-in" below. The rest of this section is the local-profile rule.

`-profile` and `-identity` select the profile as they do for every other command (`AGENTBUS2_PROFILE`, then `.local/agentbus2.json`, then `default`). The identity order is the TUI's own: `-identity`, `AGENTBUS2_IDENTITY`, the profile's `identity` only when the profile was not selected by the repository file (so inside a repository the TUI is not the repository's agent identity), the profile's only credential (also skipped when the repository file selected the profile, because it is the agent's), then the OS login name (a Windows `DOMAIN\name` is cut to `name`, a `/` becomes `-`, and an unknown user leaves the "identity is required" error). There are no other flags: v1's `-config` and `-as` are rejected (exit 2), and so is a positional argument. `-h` prints the flags and exits 0.

In local mode the TUI also runs the maintenance loop, as v1's TUI ran its background loops; when several local processes run it, a lease row in the database lets one tick at a time. A server runs its own maintenance, so server mode does not.

Exit codes: 0 on a clean quit, 1 with `agentbus2: <error>` on stderr when the session is lost or the program fails, 2 for a usage or profile/identity resolution error.

### Server profiles and sign-in

On a server profile `tui` does not select an agent identity: the profile needs an `oidc` setting, and the identity is the one the server binds to the signed-in email (Decision 11; spec 5.3). `-identity` is refused (exit 2); `AGENTBUS2_IDENTITY`, the profile's `identity` and the profile's credentials, which belong to agents, are ignored; a server profile without `oidc` exits 2 naming the missing setting. Local profiles are unchanged.

```json
{"profiles": {"lab": {"mode": "server", "server": "https://bus.example.com",
  "oidc": {"issuer": "https://idp.example.com", "client_id": "...", "redirect_port": 8765, "scopes": ["openid", "email"]}}}}
```

`oidc` is valid in server mode only. `issuer` is an absolute `https` URL; `http` is accepted only for a loopback host (`127.0.0.1`, `::1`, `localhost`, for tests with a fake provider), because the authorization code and the PKCE verifier must not cross a network in cleartext. `client_id` is the provider's public client (no secret). `redirect_port` (1 to 65535) is the fixed port of the loopback redirect `http://127.0.0.1:<port>/callback`, which the provider's client registration must list exactly. `scopes` is a non-empty list of scope names without whitespace.

With no usable session the TUI prints `agentbus2 tui: sign in at <url>` on stderr, opens the system browser at that URL (authorization code flow with PKCE) and waits for the callback for up to 5 minutes. It exchanges the provider's access token at the server and caches the server's bearer in `<data dir>/tokens/<profile>/human/session.json` (mode 0600, directory 0700; an owner-only ACL on Windows), together with the provider's refresh token when one was issued. The corporate token itself is never stored or logged. The next start reuses the cached bearer until 60 seconds before it expires, then refreshes through the provider's refresh token without the browser, and reopens the browser only when there is none or the provider rejects it. `agentbus2 login -profile P -human` always runs the browser sign-in and prints `logged in as <identity> at <server> (token expires <time>)`.

The sign-in URL is announced only before the screen opens. A bearer the server refuses mid-session is refreshed once; when that needs the browser, the request that triggered it (a screen update, a send) blocks for up to 5 minutes, with nothing shown on the screen, until the sign-in completes or times out. The TUI gives its synchronous bus calls a 10 second deadline; the sign-in ignores that deadline, so the browser has the full 5 minutes, but the call that triggered it fails with a timeout and the next one uses the new bearer. Quitting stops a sign-in only while the 10 second deadline has not yet fired (quit cancels the call's context); once it has fired, the wait for the browser runs on until the sign-in completes, its 5 minutes pass or the process exits. Run `agentbus2 login -human` before that happens if the session is long.

### Trying the sign-in against a local Dex

For manual testing against a local server, [Dex](https://dexidp.io) satisfies the token exchange: with a public client it issues a JWT access token (`RS256`, `iss` the Dex issuer, `aud` the client id, `exp`, `email`, `sub`) and, with the `offline_access` scope, a refresh token. Run Dex with a static password user and a public client whose redirect is `http://127.0.0.1:<redirect_port>/callback`:

```yaml
issuer: http://localhost:5556/dex
storage: {type: sqlite3, config: {file: /var/dex/dex.db}}
web: {http: 0.0.0.0:5556}
oauth2: {skipApprovalScreen: true}
enablePasswordDB: true
staticPasswords:
  - {email: you@example.com, hash: "<bcrypt $2a$ hash>", username: you, userID: local-human-1}
staticClients:
  - {id: agentbus2-tui, name: agentbus2 TUI, public: true, redirectURIs: ["http://127.0.0.1:8765/callback"]}
```

Server side, in a container next to Dex: `AGENTBUS2_SERVER_ISSUERS='[{"name":"dex","issuer":"http://localhost:5556/dex","jwks_uri":"http://dex:5556/dex/keys","audience":"agentbus2-tui","algorithms":["RS256"],"claims":{"email":"email","subject":"sub"}}]'` (the `issuer` is what the token says as seen from the host; `jwks_uri` is how the server container reaches Dex; Dex puts the client id in `aud`, so the entry's `audience` is the client id), then `agentbus2 server admin users add -issuer dex -email you@example.com`. Client profile: `"oidc": {"issuer": "http://localhost:5556/dex", "client_id": "agentbus2-tui", "redirect_port": 8765, "scopes": ["openid", "email", "offline_access"]}`. Notes: without `offline_access` Dex issues no refresh token, so every expiry reopens the browser; Dex v2.46.0 and later (checked up to the 2026-10 `latest`) hang on a refresh-token request (a nested storage transaction in the refresh rotation; the token endpoint never answers, with the memory and sqlite stores alike; the local stack pins v2.45.1), so a hang on the refresh step is the provider, not the client.

### Terminal

The TUI is built on Bubble Tea v2 (`charm.land/bubbletea/v2` v2.0.10, Bubbles v2.2.1, Lip Gloss v2.0.6). It draws on the alternate screen and quits on `q` or `ctrl+c`. Only `agentbus2 tui` talks to the terminal: once it starts it asks for the background color (OSC 11), which picks the compose line's light or dark colors (dark until the terminal answers), and Bubble Tea asks for its own capabilities. No other command queries the terminal; with Bubble Tea v1 every `agentbus2` process did so at package init and stalled for up to 5 s on a pty that never answered (post-build decision 2; `TestVersionOnSilentPTY` proves the fix).

Colors follow the terminal's color profile: on a terminal without color (`NO_COLOR`, `TERM=dumb`, or output that is not a terminal) tag chips fall back to dim `#tag` words. The TUI turns on Unicode core mode 2027 so Bubble Tea measures emoji the way the layout does; a terminal without that mode ignores it.

Terminal.app draws the `emoji` set's agent gear (`⚙️`) and the in-progress stopwatch (`⏱️`) two cells wide but advances the cursor one, while Lip Gloss and the renderer count two, so every cell after the glyph would land one column left (the rail border broke on gear rows). Bubble Tea v1 covered the gap with a cursor-forward escape inside the string; Bubble Tea v2's renderer drops every escape but SGR and OSC 8, so when `TERM_PROGRAM` is `Apple_Terminal` the TUI pads the two icons with U+200B (zero width space) instead: Terminal.app advances one column for it, Lip Gloss and the renderer count it zero. Other terminals get the plain emoji. `TERM_PROGRAM` does not cross ssh: for a TUI on a remote host viewed in Terminal.app set `icon_map` `agent` to `"\u2699\ufe0f\u200b"` and `task_in_progress` to `"\u23f1\ufe0f\u200b"` (`padIcon` adds the space), or use `nerdfont`.

### Identity

The TUI opens a session with the host's short name as the hint (the hostname lowercased and cut at the first `.`), so a human running it as identity `eric` on `MacMini.local` shows as `eric/macmini`, and as `eric/laptop` on another host (`eric/macmini-2` for a second TUI on one host). A hostname that is unavailable or fails the name rule (empty, over 128 bytes, a `/` or a control character) falls back to the hint `tui` (`eric/tui`). The status bar says `as eric`, the identity; the health overlay shows the session's display name. Agents can tell the TUI from the identity's agent sessions. The session is closed on quit, which frees the name at once. A TUI that is killed leaves its session lapsed until the server's retention removes it, and the next start is then `eric/macmini-2`; nothing is lost, because a new session catches up from the identity's acked mark.

Subscriptions and the acked mark belong to the identity, not the session. A human who runs the TUI under the identity the agents use shares their subscriptions and acks with them. Use a distinct identity for the human (`-identity eric`). When `Identities` shows other live sessions of the TUI's identity, `agentbus2 tui` prints one line on stderr before the screen opens and repeats it in the health overlay:

```
agentbus2 tui: identity eric has 1 other live session(s); subscriptions and acks are shared with them (pass -identity for a separate name)
```

On a local profile, when nothing else selects an identity, and also whenever the repository file selected the profile (its `identity` and its single credential are skipped, so in a `$HOME` repository the agent `efitz-home` and the human `efitz` stay apart), `tui` (and only `tui`) falls back to the OS login name (source `os-user`, v1's `tui_name`). On a server profile there is no such fallback (see "Server profiles and sign-in"). In a local profile the OS-login row is marked human (an identity named with `-identity` is never turned human); a row that already holds keys cannot be, and the TUI exits 2 asking for `-identity`. Every other command has no such fallback. The login name is the human's: `agentbus2 mcp` refuses to run under it (Decision 16), and `agentbus2 init` in the home directory names the agent `<user>-home` (Decision 15).

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

The section of a channel comes from its kind, never from its name. Rows show the full name. `←`/`→` or `enter` on a heading collapses or expands its section; a collapsed heading shows `(count[, N unread])`. Collapsing lasts until the TUI quits. `home` selects the `channels` heading. A `sessions` row is a rail stop. Selecting it replaces the main pane with that session's details: display name, identity (`unknown` for an idle row, whose identity the TUI never learned), session id (live rows only), and `live` or `idle, last seen <time>`. The details come from what the rail already holds, so selecting a row makes no bus call; there is still no inbox view and no messaging from a session row (ADR 0006), and `i`, `enter`, `s`, `d` and `r` do nothing on one. When a refresh removes the selected row, the selection moves to the `sessions` heading.

Rail markers:

- `(off)` after a channel name: you are not subscribed to it (`s` toggles).
- `(no access)` in the dim style: the channel's ACL denies you read. `ListChannels` shows every channel, so the rail lists it, but nothing subscribes to it or reads its history, and selecting it shows `no read access to <name>`.

`<` and `>` narrow and widen the rail and save the width as `tui.rail_width` in `config.json`.

A message from a human sender (its `sender_kind` is `human`, a person signed in through a corporate provider) is drawn like your own: the `user` icon in the user color. Messages from agents keep the `agent` icon in the agent color. Session rows and task owners carry no message and keep the old rule: the user icon only for your own identity.

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

`h` shows the version, the mode line (`local · <sqlite path>` or `server · <url>`), your identity, display name and session id, the live sessions, the channels by kind, the receive state (last batch, gaps, last error), the server line (the server's own status, version, store engine and schema, and embedding state, from its health endpoint, fetched each time the overlay opens, so a reopen shows no stale answer; `server unavailable · <error>` when the endpoint fails, never a toast), the administrator block when your identity is an administrator (capacity notices, the embedding backlog and errors, storage, and the maintenance line: lock held, held elsewhere or unknown, the time of the last tick, the job count and failures, with one line per failed job and a `lock error:` line when the lock attempt failed), the warnings printed at start, the theme with each resolved value, the icon set, the config path and the effective `tui` settings. A non-administrator never calls the admin operations (`Status`, `Usage`) and sees a line saying so.

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
| `user` | your own name and human senders | `yellow` |
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
| `agent` | other agent senders and sessions | U+EE0D |
| `user` | your own name and human senders | U+F007 |
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
- Identity comes from `-identity`, `AGENTBUS2_IDENTITY` and the profile (Decision 71, with the repository-file exception above), then the OS login name as the last step, on a local profile; on a server profile it comes from the corporate sign-in; not from `tui_name` or `-as`.
- Names shown for messages and sessions are the server's (`Message.Display`, `Session.DisplayName`; ADR 0003).
- Task release is its own operation (`u`), and assigning picks from identities (owners are identities).
- The TUI reads from `Watch` and `Receive` with acks (Decision 83) rather than a blocking `Receive`, and has no per-session repo or context.

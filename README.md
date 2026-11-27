# Tasmota Lights for Prusa Connect

A 3D printer spends most of a print doing nothing you need to know about, and
then suddenly wants you: the filament ran out, the print finished, something
went wrong. This module turns a Tasmota-controlled LED strip into a status light
you can read from across the room. Orange while it warms up, white while it
prints (which also lights the camera), blue when it wants filament, green when
it's done.

It is a [Prusa Connect](https://github.com/prusa3d/prusa-connect) service
module: a small program that Prusa Connect starts, supervises and feeds with
the printer's state. It was written for a Prusa MINI and an LC04 RGBW controller
running Tasmota, so that's the setup it has actually been run on. Anything else
is untested.

## How It Decides What to Show

Every printer Prusa Connect serves gets a **mood**, and each mood has a
**scene**: a list of Tasmota commands. When the mood changes, the scene is sent.
The moods are checked in this order, and the first one that matches wins:

| mood | when | default scene |
| --- | --- | --- |
| `offline` | Prusa Connect has no link to the printer | both lights off |
| `error` | the printer reports `ERROR` | red |
| `filament` | a dialog is up whose title or text contains a word from `filament_words`, or whose code is in `filament_codes` | blue |
| `attention` | the printer reports `ATTENTION`, or any other dialog is up | amber |
| `paused` | `PAUSED` | amber and white |
| `finished` | `FINISHED` | green and white |
| `stopped` | `STOPPED` (a cancelled print) | both lights off |
| `warming` | a heater has a target it hasn't reached yet | orange |
| `printing` | `PRINTING` | white only |
| `idle` | anything else | both lights off |

The order is deliberate: a state that needs a person beats one that doesn't, so
a filament prompt in the middle of a print shows as `filament`, not `printing`.

### Warming Up Isn't a Printer State

So how does it know the printer is warming up? A Buddy board has no such state.
Preheating happens inside `PRINTING` at the start of a print, and inside `IDLE`
if you preheat from the printer's own menu. So the module watches the nozzle
and bed temperatures against their targets instead.

The naive rule, "below target means warming", makes the lights flicker: a part
fan can pull the nozzle a few degrees under its target in the middle of a print.
So warming is latched. Once the printer reaches a pair of targets (within
`warm_margin` degrees), it stays "warmed up" until a target changes. A new
target, like the MINI going from its 170 °C probing temperature up to the print
temperature, starts warming again. A sag under the same target doesn't.

This has been checked on a real MINI with a 50 °C nozzle target. The lights went
orange 3 seconds after the command, switched off when the nozzle reached 45.5 °C
(inside the 5 degree margin), and stayed off when it dipped back to 44.5 °C.

### What It Leaves Alone

A scene is sent **once**, when the mood changes. If you change the lights with
the IR remote halfway through a print, your choice stays until the printer does
something new. If Tasmota doesn't answer (it's rebooting, or off WiFi), the
scene is sent again every 15 seconds until it goes through.

With more than one printer, the lights show whichever printer most needs a
person, in the order of the table above. Set `printer` to follow just one.

## What You Need

- **Prusa Connect v0.1.0-beta.2** running somewhere it can reach the Tasmota
  device. The module was built against commit `d4e7673`, between beta.1 and
  beta.2, and runs unchanged on beta.2: after the upgrade it came back on its
  own and drove the lights through a whole print start (see What's Been
  Tested). It hasn't been rebuilt against beta.2's SDK, so the `.core/sdk`
  copy is still the `d4e7673` one.
- **A Tasmota device with two lights**: the RGB channels as light 1 and the
  white channel as light 2. On an RGBW controller that means
  `SetOption37 128`, which splits them. The default scenes use `Power1` and
  `Color1` for the RGB light and `Power2` for the white one.
- **No web password on the Tasmota device.** The module sends plain
  `/cm?cmnd=` requests with no credentials (see Known Limitations).
- **Go 1.26.11** and a checkout of Prusa Connect, to build it.

## Building It

The Prusa Connect SDK isn't published yet, so the module builds against a copy
of it in `.core/sdk`, which `go.mod` points at with a `replace` line. Copy the
`sdk` folder out of your Prusa Connect checkout first:

```bash
mkdir -p .core
cp -r ../prusa-connect/sdk .core/sdk
git -C ../prusa-connect rev-parse HEAD > .core/SDK_COMMIT
```

`.core/` is ignored by git, because it's somebody else's code. `SDK_COMMIT`
records which version you built against, for when the SDK changes under you.

Then run the tests and build a release:

```bash
make release
```

You should see the tests pass and then the build:

```
go test -race ./...
ok  	github.com/robeeejay/prusa-tasmota-lights	1.800s
rm -rf release && mkdir release
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags=-s -o release/tasmota-lights_linux_arm64 .
cp module.yaml release/
cd release && shasum -a 256 module.yaml tasmota-lights_linux_arm64 > checksums.txt
```

`release/` is now the directory `prusa-connect module add -from` reads: the
manifest, one binary, and the checksums of both. Only `linux_arm64` is built,
because that's what the Raspberry Pi 4 it runs on needs. Add a platform to
`platforms` in `module.yaml` and a line to the `release` target if yours is
different.

## Installing It

### Prusa Connect in Docker

If Prusa Connect runs in the `prusa_connect` container, copy the release and the
install script to the server:

```bash
make copy PI=you@your-server
```

Then on the server, run the script it prints:

```bash
sudo bash ~/tasmota-lights-0.1.0/install.sh
```

It needs root because the container is only reachable through `docker`. The
script copies the release into the container's `/tmp`, runs `module add` with
`-approve`, and restarts the container, because a running server only reads
the install record when it starts.

`-approve` gives the module the one permission `module.yaml` asks for,
`observe`, which lets it read the printers and their events. It can't send a
printer a command.

**BE AWARE** that the lights change as soon as the module starts. An idle printer
gets the `idle` scene, which switches both lights off by default.

### Prusa Connect Without Docker

Run `module add` with the binary the server already is, then restart the
server:

```bash
prusa-connect module add github.com/robeeejay/prusa-tasmota-lights@0.1.0 -from ./release -approve
```

This one is untested here. It's the command `install.sh` runs inside the
container, without the container around it.

## Settings

Every setting has a default, so the module starts without any. Change them in
Prusa Connect under **Settings**, **Extensions**, on the module's **Settings**
tab, or in the `config:` block of its declaration.

| setting | type | what it does | default |
| --- | --- | --- | --- |
| `tasmota_url` | url | The Tasmota device, as the address its web page opens on. | `http://192.168.178.54` |
| `printer` | string | The printer to follow, by id or by name. Empty follows every printer. | empty |
| `warm_margin` | number | How many degrees under its target a heater counts as warmed up. | `5` |
| `filament_codes` | list | Dialog codes that mean the printer wants filament. | `[]` |
| `filament_words` | list | Words in a dialog's title or text that mean the printer wants filament. | `["filament"]` |
| `scenes` | object | The Tasmota commands for each mood. | see `module.yaml` |

The default `tasmota_url` is the address of the device this was written for.
You'll almost certainly want to change it.

A scene is Tasmota commands separated by semicolons, sent in order:

```yaml
scenes:
  finished: "Power1 ON; Color1 00FF00; Power2 ON"
  idle: ""
```

An empty scene leaves the lights as they are, so `idle: ""` above means
"don't touch them when nothing is happening". You only have to write the moods
you want to change: the rest keep their defaults.

Each command goes to Tasmota as its own request, not as one `Backlog`. That's
three requests where one would do, but Tasmota answers `Backlog` with `{}` even
when a command in it is misspelled, so a typo in a scene would fail without a
word. One at a time, a misspelled command comes back as an error you can see.

## Checking It Works

The module answers one route, which says what it sees and what it last sent:

```
GET /api/v1/modules/com.github.aux.robeeejay.prusa-tasmota-lights/status
```

Open it in a browser where you're signed in to Prusa Connect. You should see
something like this (trimmed):

```json
{
  "configured": true,
  "lights": {"mood": "idle", "printer": "f979946b-…", "sent_at": "2026-11-24T21:34:56Z"},
  "printers": [{"name": "MINI …", "connected": true, "state": "IDLE", "mood": "idle",
                "nozzle": 11.5, "nozzle_target": 0, "bed": 12.3, "bed_target": 0}]
}
```

If `lights.error` is set, that's what Tasmota answered when a scene didn't go
through. When a printer stops on a dialog, `printers[].dialog` shows its code,
title and text. That's the place to find the code for `filament_codes` if a
filament prompt shows up as `attention` instead.

The module's own log lines (`the lights show a new mood`, `the lights did not
change`) end up in Prusa Connect's log.

## Removing It

```bash
sudo docker exec prusa_connect /prusa_connect module remove sink:tasmota-lights
sudo docker restart prusa_connect
```

Prusa Connect's documentation says this removes the declaration, the installed
files and the permission `-approve` granted. It hasn't been run here yet. The
lights stay in whatever scene they were last sent.

## What's Been Tested

- **Unit tests:** 25 tests, run with Go's race detector, cover the mood
  rules, the warming latch, the Tasmota client (including the answers Tasmota
  gives for an unknown command and for a web password), choosing between
  printers, and the sender. Four of the checks (the warming latch, Tasmota's
  refusals, settling after telemetry, and not resending a scene) were
  confirmed by breaking the code and watching the test fail. The rest weren't.
  One test, "the same mood is sent once", first passed
  for the wrong reason: it asked for the same mood faster than the sender read
  the requests, so the repeat was never seen at all. It now spaces them out.
- **On the real MINI:** the module installed, started, followed the printer's
  live temperatures, sent the `idle` scene, and showed `warming` and then
  `idle` during the 50 °C test above.
- **A real print, on Prusa Connect beta.2:** a PLA print sent from PrusaSlicer.
  Here is every mood change from Prusa Connect's log, starting with the
  restart that brought in beta.2:

  | time | mood | what happened |
  | --- | --- | --- |
  | 20:18:16 | `offline` | Prusa Connect restarted, and the MINI hadn't reconnected |
  | 20:18:17 | `idle` | the MINI was back |
  | 20:23:53 | `printing` | the print started, before its heater targets arrived |
  | 20:23:54 | `warming` | the targets arrived, 1.5 seconds later |
  | 20:26:11 | `printing` | the first pair of targets was reached |
  | 20:27:43 | `warming` | the nozzle got a new target, the print temperature |
  | 20:28:31 | `printing` | reached, and the white light stayed on |

  That's the warming latch doing its job: the MINI warms up once for probing
  and again for printing, and both showed as `warming`. Half an hour later the
  nozzle sat at 220.1 °C against 220 °C, and asking the Tasmota device itself
  answered `POWER1` off and `POWER2` on, which is the printing scene.

  One thing I didn't expect: the first 1.5 seconds of the print showed
  `printing`, because the printer reports PRINTING a moment before it sets
  its heater targets. So the white light flashes on before the orange. It's
  harmless, but it's a flash, and it's in Future Work.
- **Not seen yet on the real printer:** `paused`, `finished`, `stopped`,
  `error`, and the filament prompt. Whether the MINI's "load filament" dialog
  actually contains the word "filament" is a guess until it's watched happen.

## Known Limitations

- **No Tasmota web password.** A device with one answers every command with
  `Need user=<username>&password=<password>`. The module reports that as an
  error, but has no setting to send the credentials.
- **One Tasmota device.** All printers share one set of lights.
- **Port 80 only.** `module.yaml` declares port 80 for the Tasmota address,
  which is where Tasmota's web server listens unless you've moved it.
- **The SDK is copied, not fetched.** Until Prusa Connect publishes its SDK,
  building needs a checkout of it.

## Future Work

- **Confirm the filament prompt on a real print**, then list its code in the
  defaults if the word "filament" isn't on the screen. It's not done because it
  needs a print or a filament load to watch.
- **Tasmota credentials**, as a `secret` setting. Not done because the device
  this was written for has no web password.
- **No white flash at the start of a print.** The printer reports PRINTING
  about 1.5 seconds before its heater targets, so `printing` shows briefly
  before `warming`. Waiting a couple of seconds before showing `printing`
  would hide it. Not done yet because it was only just seen, and it needs a
  test for the wait.
- **Rebuild against beta.2's SDK.** The module runs on beta.2 as built from
  `d4e7673`. Not done because nothing has needed it yet.

That's it. If the lights do something odd, the status route above is the first
place to look. It shows what the module thought the printer was doing, which
is usually where the odd bit is.

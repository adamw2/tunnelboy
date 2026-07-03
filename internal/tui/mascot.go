package tui

import "strings"

// Tunnel Boy — original ASCII mascot in the spirit of a certain
// vault-dwelling thumbs-up guy. Shown while waiting on slow launches.
// Frames animate eyes (blink/wink) and a sparkle near the thumb.
var mascotFrames = []string{
	`   .------.
  / .----. \
 | | ^  ^ | |
 | |  o o | |
 | |  \__/| |       .-.
  \ '----' /        | |
   _|_||_|_       __| |
  / |    | \     / .--'
 /  | 76 |  \___/ /
|   |    |       /
 \  '----' \____/
  |__|  |__|
  (___)(___)`,
	`   .------.
  / .----. \
 | | ^  ^ | |
 | |  o o | |     *
 | |  \__/| |       .-.
  \ '----' /        | |
   _|_||_|_       __| |
  / |    | \     / .--'
 /  | 76 |  \___/ /
|   |    |       /
 \  '----' \____/
  |__|  |__|
  (___)(___)`,
	`   .------.
  / .----. \
 | | ^  ^ | |
 | |  o - | |           *
 | |  \__/| |       .-.
  \ '----' /        | |
   _|_||_|_       __| |
  / |    | \     / .--'
 /  | 76 |  \___/ /
|   |    |       /
 \  '----' \____/
  |__|  |__|
  (___)(___)`,
	`   .------.
  / .----. \
 | | ^  ^ | |
 | |  o o | |
 | |  \__/| |       .-.
  \ '----' /    *   | |
   _|_||_|_       __| |
  / |    | \     / .--'
 /  | 76 |  \___/ /
|   |    |       /
 \  '----' \____/
  |__|  |__|
  (___)(___)`,
	`   .------.
  / .----. \
 | | ^  ^ | |
 | |  - - | |
 | |  \__/| |       .-.
  \ '----' /        | |
   _|_||_|_       __| |
  / |    | \     / .--'
 /  | 76 |  \___/ /
|   |    |       /
 \  '----' \____/
  |__|  |__|
  (___)(___)`,
}

// Mascot returns one animation frame of Tunnel Boy, indented and
// styled. frame can be any non-negative counter; it wraps.
func Mascot(frame int) string {
	f := mascotFrames[frame%len(mascotFrames)]
	return TextStyle.Render("    " + strings.ReplaceAll(f, "\n", "\n    "))
}

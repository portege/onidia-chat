package main

// farewells.go - the goodbye that closes the show.
//
// Whenever the character signs off - the chat window is closed, or the pet is
// sent away with the Haiya! button - she picks one farewell at random, waves,
// puts it in her speech bubble and says it out loud before the process (or
// her) actually goes away.
//
// Everything the goodbye touches is optional: with no pet running, no audio
// engine, or a dead TTS API the line still lands in the chat log and the
// shutdown continues after a bounded wait, so a farewell can never wedge an
// exit.
//
// WHY THE ASCII FILTERING: both renderers are hand-built 5x7 bitmap fonts -
// onidia-chat's font5x7 (font.go) and the pet's fx.font5x7
// (onidia/internal/fx/font.go). A rune with no glyph in those tables is
// painted as blank space, so a goodbye written in its own script ("さようなら",
// "До свидания", "إلى اللقاء") would come out of the bubble as a row of
// holes. The list therefore stores each phrase romanised (which is also the
// best the English-voiced Typecast TTS can do with it), and asciiFarewell
// still cleans up anything non-ASCII a future edit drops in, so a native-
// script entry degrades to its readable part instead of vanishing.

import (
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"
)

const (
	// farewellSpokenGrace caps how long a shutdown waits for the goodbye to
	// be spoken before continuing anyway. Long enough for a one-line phrase
	// plus the Typecast round trip, short enough that a dead TTS API cannot
	// make the close button feel broken.
	farewellSpokenGrace = 6 * time.Second

	// farewellQuietPause is how long a mute/silent shutdown keeps the
	// goodbye on screen: the bubble is already up (nothing has to wait for
	// audio), this just gives the line a moment to be read.
	farewellQuietPause = 2 * time.Second

	// farewellMood is the say-line mood tag: she leaves in a good mood.
	farewellMood = "happy"

	// farewellWaveCmd makes the pet raise a hand as she says it. "bye" and
	// "goodbye" are aliases of the pet's wave action, which is exactly the
	// gesture the persona instructions call out for farewells.
	farewellWaveCmd = "action wave"

	// farewellFallback is the last resort when the random pick sanitises
	// down to nothing (an entry written entirely in unrenderable runes).
	farewellFallback = "Bye-bye! See you soon!"
)

// farewellLines is the goodbye pool. Multilingual, romanised, and salted with
// the lines she would never pass up - one phrase per entry, short enough to
// fit a bubble page. Keep it ASCII (see the header comment); anything else is
// folded or dropped by asciiFarewell at pick time.
var farewellLines = []string{
	// Everyday English, including the ones nobody grows out of.
	"Bye-bye! Come back soon!",
	"See you later, alligator!",
	"In a while, crocodile!",
	"Gotta go, Buffalo!",
	"Bye bye, butterfly!",
	"Stay gold, kid!",
	"Peace out!",
	"Catch you on the flip side!",
	"Don't be a stranger!",
	"Toodle-oo, Cheerio, Ta-ta!",
	"Smile and wave!",
	"Over and out!",
	"Signing off. Over!",
	"End of transmission!",
	"Powering down... nighty night!",
	"That's all, folks!",

	// Romance languages.
	"Adios, amigo!",
	"Hasta la vista, baby!",
	"Hasta pronto!",
	"Hasta luego, companero!",
	"Chao chao!",
	"Tchau, tchau!",
	"Ate amanha!",
	"Ate logo!",
	"Ciao, bella!",
	"Arrivederci a presto!",
	"A presto!",
	"Au revoir!",
	"A bientot, d'accord?",
	"Salut salut!",
	"Tot ziens!",
	"Doeg doeg!",

	// Germanic and Nordic.
	"Tschuss!",
	"Auf Wiedersehen!",
	"Bis bald!",
	"Servus und griass di!",
	"Tschow, bis dann!",
	"Hej da!",
	"Hade, da!",
	"Ha det bra!",
	"Farvel, sa vi ses!",
	"Moi moi!",
	"Hei hei!",
	"Aga, bless bless!",
	"Totsiens!",
	"Ba baai!",

	// Slavic.
	"Dasvidaniya!",
	"Do svidaniya, druzya!",
	"Poka-poka!",
	"Do widzenia!",
	"Czesc, na razie!",
	"Dovidenia, kamo!",
	"Nasvidanou, mej se!",
	"Zbohom, maj sa!",
	"Zhiveli, druzhishche!",

	// Finno-Ugric, Baltic, Balkan, Greek.
	"Head aega!",
	"Iki, iki pasimatymo!",
	"Viszontlatasra!",
	"Szia, szia!",
	"La revedere, pa pa!",
	"Yassas, re please!",
	"Antiyo, filou!",
	"Zdravei, do skorho!",

	// Turkic, Persian, Caucasian.
	"Gorusuruz, hosca kal!",
	"Gule gule!",
	"Khoda hafez!",
	"Khuda hafiz, janam!",
	"Gorusene, bay bay!",

	// South Asian.
	"Alvida!",
	"Phir milenge!",
	"Aavjo! Namaste!",
	"Poyittu vanthudalam!",
	"Kalak borossa ai!",

	// East and Southeast Asian.
	"Sayonara, mata ne!",
	"Ja ne, mata ashou!",
	"Zai jian, ming tian a!",
	"Yasumenasai, oyasumi!",
	"Annyeonghi gaseyo!",
	"Annyeong, jal ga!",
	"Tam biet, hen gap lai!",
	"La gon krub!",
	"Sampai jumpa lagi!",
	"Paalam, ingat diri!",
	"La gaa, phop kan mai!",

	// Afro-Asiatic and Hebrew.
	"Ma'a as-salama!",
	"Ila al-liqaa!",
	"Lehitraot, habibi!",
	"Shalom, lehitraot!",
	"Khoda negahdar!",
	"Beshalom, modeh!",

	// Sub-Saharan African.
	"Kwaheri, rafiki!",
	"Sala kahle, hamba kahle!",
	"O dabo!",

	// Celtic.
	"Slan, a chairde!",
	"Mar sin leat!",
	"Hwyl fawr, ffrind!",

	// Screen farewells she stole from the telly.
	"See you, space cowboy...",
	"So long, and thanks for all the fish!",
	"May the Force be with you!",
	"Live long and prosper!",
	"Beam me up, Scotty!",
	"To infinity and beyond!",
	"I'll be back!",
	"Keep the change, ya filthy animal!",
	"Party on, dudes!",
	"Stay frosty, soldier!",
	"I'll be seeing you, see you next month...",
	"And who cares? No big deal, I move on!",
}

// asciiFolds reduces the characters our 5x7 tables do not carry to a form
// they can draw: accented Latin to its bare letter, curly punctuation to
// plain ASCII. Deliberately small - only what plausibly turns up in a
// romanised farewell. Runes missing here are dropped by asciiFarewell.
var asciiFolds = map[rune]string{
	'À': "A", 'Á': "A", 'Â': "A", 'Ã': "A", 'Ä': "A", 'Å': "A",
	'Ç': "C", 'È': "E", 'É': "E", 'Ê': "E", 'Ë': "E",
	'Ì': "I", 'Í': "I", 'Î': "I", 'Ï': "I", 'Ñ': "N",
	'Ò': "O", 'Ó': "O", 'Ô': "O", 'Õ': "O", 'Ö': "O", 'Ø': "O",
	'Ù': "U", 'Ú': "U", 'Û': "U", 'Ü': "U", 'Ý': "Y",
	'å': "a", 'ä': "a", 'ç': "c", 'è': "e", 'é': "e", 'ê': "e", 'ë': "e",
	'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ñ': "n",
	'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ý': "y", 'ÿ': "y",
	'ß': "ss", 'æ': "ae", 'Æ': "AE", 'œ': "oe", 'Œ': "OE",
	'ą': "a", 'č': "c", 'ć': "c", 'ď': "d", 'đ': "d", 'ě': "e",
	'ę': "e", 'ģ': "g", 'ī': "i", 'ķ': "k", 'ļ': "l", 'ł': "l",
	'ņ': "n", 'ř': "r", 'ś': "s", 'ş': "s", 'š': "s", 'ť': "t",
	'ů': "u", 'ź': "z", 'ż': "z", 'ž': "z",
	'ā': "a", 'ē': "e", 'ō': "o", 'ū': "u", 'ğ': "g",
	'‘': "'", '’': "'", '‚': "'", '‛': "'",
	'“': `"`, '”': `"`, '„': `"`, '«': `"`, '»': `"`,
	'–': "-", '—': "-", '―': "-", '…': "...",
	' ': " ", '·': " ", 'ª': "a", 'º': "o",
}

// asciiFarewell folds one farewell to what the bitmap fonts can draw. Both
// renderers (onidia-chat's font5x7 and the pet bubble's fx.font5x7) fall back to
// blank space for a rune they have no glyph for, and the say-FIFO is
// line-oriented, so: line breaks and tabs become spaces, control characters
// go, ASCII passes through, folded runes are replaced, and anything else
// non-ASCII is dropped instead of painted as a hole. Whitespace collapses, and
// a farewell left with nothing renderable - no letters or digits at all, only
// the punctuation a translated phrase leaks through - comes back "", so
// randomFarewell substitutes the built-in line instead of a bubble of commas.
func asciiFarewell(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ') // one line, always: the FIFO splits on \n
		case r < 0x20 || r == 0x7f:
			// control characters: the pet's sanitizer eats them anyway
		case r <= 0x7f:
			b.WriteRune(r)
		default:
			b.WriteString(asciiFolds[r]) // unknown runes fold to ""
		}
	}
	joined := strings.Join(strings.Fields(b.String()), " ")
	for _, r := range joined {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return joined // a word survived the folding
		}
	}
	// Only punctuation came through ("さようなら" -> ", !"): that is not a
	// farewell, so report nothing and let randomFarewell pick another line.
	return ""
}

// randomFarewell picks this shutdown's goodbye. The pool is grouped by
// language family, not weighted, so "Poka-poka!" is exactly as likely as
// "Live long and prosper!". Never empty, always renderable. The global
// math/rand source is seeded at startup by the runtime (Go 1.20+), so
// consecutive sessions really do say different things.
func randomFarewell() string {
	if len(farewellLines) == 0 {
		return farewellFallback
	}
	if line := asciiFarewell(farewellLines[rand.Intn(len(farewellLines))]); line != "" {
		return line
	}
	return farewellFallback
}

// farewellSigner delivers the goodbye over the three channels a normal reply
// already uses - the chat log, the pet's say-FIFO and text-to-speech - plus
// the pet's command FIFO for the wave. Every field is optional: the zero
// value picks a phrase, logs it, and returns without touching a pipe.
type farewellSigner struct {
	TTS     *TTS   // speech engine; nil or disabled = silent goodbye
	Muted   bool   // the dialog's MUTE SPEECH checkbox
	UI      *UI    // append the line to this chat log (nil = leave UI alone)
	Paint   func() // repaint right after appending (used with UI only)
	Name    string // sender label for the chat-log line
	SayPipe string // pet say-FIFO ("" = no pet to speak to)
	CmdPipe string // pet command FIFO, for the wave ("" = none)
}

// signOff picks one farewell and delivers it, then holds the caller for as
// long as it takes to get the words out loud - capped by farewellSpokenGrace,
// so a hung TTS API slows an exit by seconds, never indefinitely, and a dead
// pet (no FIFO reader) costs nothing at all. why names the shutdown in the
// log ("window close", "pet quit"). Returns the line said ("" if none).
//
// Call it on the main loop when UI is set - UI state is main-loop-only in
// this program; the pet-quit path runs it on a goroutine with UI left nil so a
// slow audio fetch can never freeze the window.
func (f *farewellSigner) signOff(why string) string {
	line := randomFarewell()
	if line == "" {
		return ""
	}
	log.Printf("farewell(%s): %q", why, line)

	// Keep it in the scrollback so the goodbye survives in the transcript,
	// and paint it before the wait below parks the loop.
	if f.UI != nil {
		f.UI.AddMsg(f.Name, line)
		if f.Paint != nil {
			f.Paint()
		}
	}

	// She raises a hand on the way out, when a pet is listening.
	if f.CmdPipe != "" && petPipeReady(f.CmdPipe) {
		petCmd(f.CmdPipe, farewellWaveCmd)
	}

	bubble := buildPetSayLine(f.SayPipe, farewellMood, line, "", nil)

	// Voice path: bubble and audio share one hook pair, exactly like a
	// streamed reply - the bubble opens as playback starts and closes when
	// it ends, and that close is what releases the shutdown.
	if f.TTS != nil && f.TTS.Enabled() && !f.Muted {
		done := make(chan struct{})
		var once sync.Once
		finish := func() { once.Do(func() { close(done) }) }
		f.TTS.SpeakLine(line,
			func() { petSayLine(f.SayPipe, bubble) },
			func() {
				petClear(f.SayPipe) // same FIFO keeps open/close ordered
				finish()
			})
		select {
		case <-done:
		case <-time.After(farewellSpokenGrace):
			log.Printf("farewell: speech unfinished after %v - closing anyway", farewellSpokenGrace)
		}
		return line
	}

	// Silent path (muted, no engine, or no audio player): the bubble is up
	// now and the pet closes it by reading time, so the only thing to
	// preserve is a beat to read it before the window dies.
	petSayLine(f.SayPipe, bubble)
	time.Sleep(farewellQuietPause)
	return line
}

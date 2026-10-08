package wikitext

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Every input shape below was read from the prod corpus (eve_knowledge_v3_go_bge_prod).
func TestStripTemplates(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// --- display templates ---------------------------------------------------
		{"skill bare", "{{sk|Gunnery}}", "Gunnery"},
		{"skill mixed case name", "{{Sk|Caldari Titan}}", "Caldari Titan"},
		{"skill with level", "{{sk|Mining Upgrades|IV}}", "Mining Upgrades IV"},
		{"skill ignores named arg", "{{Sk|Tactical Shield Manipulation|IV|mult=yes}}", "Tactical Shield Manipulation IV"},
		{"required skill", "{{RequiredSkill|Spaceship Command|I}}", "Spaceship Command I"},
		{"skill alpha bar", "{{SkillAlphaBar|Talocan Technology}}", "Talocan Technology"},
		{"colour keeps text", "{{co|coral|Remote Sensor Dampeners}}", "Remote Sensor Dampeners"},
		{"colour with blank slot", "{{co|#00BFFF||C2}}", "C2"},
		{"colour keeps wiki emphasis", "{{co|red||'''Do not even talk to the COSMOS Agent'''}}", "'''Do not even talk to the COSMOS Agent'''"},
		{"colour var()", "{{co|var(--color-warning)|(RU)}}", "(RU)"},
		{"color hex", "{{color|#5188C2|(Tier 3 Gift)}}", "(Tier 3 Gift)"},
		{"ship short", "{{sh|Eagle}}", "Eagle"},
		{"ship short with size", "{{Sh|Maller|22}}", "Maller"},
		{"ship long with box", "{{Ship|Mackinaw|box}}", "Mackinaw"},
		{"triglavian", "{{triglavian|SVAROG}}", "SVAROG"},
		{"lexicon keeps the term", "{{lex|TC|Tracking Computer (see [[Turrets]])}}", "TC"},
		{"lexicon with piped link in definition", "{{lex|LSI|Large Skill Injector (see [[#Injector|Injector]])}}", "LSI"},
		{"tooltip keeps both", "{{tooltip|HAC|Heavy Assault Cruiser}}", "HAC (Heavy Assault Cruiser)"},
		{"icon with caption", "{{icon|web|24|Stasis webifier -50% to 10km}}", "Stasis webifier -50% to 10km"},
		{"icon with blank size", "{{icon|em resist||EM}}", "EM"},
		{"icon without caption", "{{icon|sig|50}}", ""},
		{"icon name only", "{{icon|caldari2}}", ""},
		{"key button", "Press {{button|Ctrl}} and {{button|F}}", "Press Ctrl and F"},
		{"damage types", "{{Damagetype|kin|th|ex}}", "Kinetic/Thermal/Explosive"},
		{"damage type upper", "{{Damagetype|EX}}", "Explosive"},
		{"damage type omni", "{{Damagetype|Omni}}", "Omni"},
		{"damage type short ki", "{{Damagetype|ki|th|em|ex}}", "Kinetic/Thermal/EM/Explosive"},
		{"security rating", "{{ColorSecurityRating|0.6}}", "0.6"},
		{"security rating named", "{{ColorSecurityRating|rating=0.7}}", "0.7"},
		{"system link", "{{SystemToSecurity|Luminaire}}", "Luminaire"},
		{"main article", "{{main|Asteroids and ore}}", "Main article: Asteroids and ore"},
		{"see also", "{{See also|Jump Freighters}}", "See also: Jump Freighters"},
		{"cubic metres", "Volume 10 {{m3}}", "Volume 10 m³"},
		{"clone state numbered", "{{Clonestate|1= omega|2= Omega state}}", "Omega state"},
		{"clone state positional", "{{Clonestate|omega|Omega clones}}", "Omega clones"},
		{"hatnote keeps the sentence", "{{Hatnote|See [[EVE University Management]] for details.}}", "See [[EVE University Management]] for details."},
		{"message box joins title and body", "{{MessageBox|In local: |Squadron Leader: You will pay for that |collapsed=yes}}", "In local: Squadron Leader: You will pay for that"},

		// --- NPC tables keep their data ------------------------------------------
		{"npc heading", "{{NPCTableHead|Wave #2}}", "Wave #2"},
		{"npc separator", "{{NPCTableSeparator|Group 2 (40 km)}}", "Group 2 (40 km)"},
		{"npc row", "{{NPCTableRow|Elite Cruiser|1|Dire Pithum Inferno}}", "(Elite Cruiser, 1, Dire Pithum Inferno)"},
		{"npc row with named note", "{{NPCTableRow|Frigate|2|Blood Raiders Cruor|ewar= Web}}", "(Frigate, 2, Blood Raiders Cruor, ewar: Web)"},
		{"npc row spaced", "{{NPCTableRow | Structure| 1 | Deactivated Acceleration Gate | note= Activates after all enemies are destroyed}}",
			"(Structure, 1, Deactivated Acceleration Gate, note: Activates after all enemies are destroyed)"},
		{"npc rows stay separated", "{{NPCTableRow|Frigate|1|A}} {{NPCTableRow|Frigate|2|B}}", "(Frigate, 1, A) (Frigate, 2, B)"},
		{"structured data skips empty and presentation args",
			"{{Missiondetails |Level=3 |Type=Encounter |Objective=Kill their high-ranking officers |Faction1=Guristas |Faction2= |image=Foo.png |DamageToDeal=}}",
			"(Level: 3, Type: Encounter, Objective: Kill their high-ranking officers, Faction1: Guristas)"},

		// --- markup that carries no text -----------------------------------------
		{"no-arg boilerplate", "Intro {{Clear}} body {{NPCTableCSS}} end", "Intro body end"},
		{"page name", "{{PAGENAME}}", ""},
		{"navigation with arg", "Text {{SistersOfEVEEpicArcNav|chapter 2}} more", "Text more"},
		{"links box", "{{Mining Links}}", ""},
		{"wave spawn marker", "{{npcwh|ssen|2x}}", ""},
		{"table of contents", "{{TOC|align= right}}", ""},
		{"named-only presentation args", "{{MainPageTile|image=ShipsLogo.png|link=Ships}}", ""},

		// --- structure ------------------------------------------------------------
		{"nested templates resolve inside out", "{{co|wheat|{{sk|Gunnery}}}}", "Gunnery"},
		{"nested in data row", "{{NPCTableRow|Frigate|1|{{sh|Rifter}}}}", "(Frigate, 1, Rifter)"},
		{"escaped pipe survives", "a {{!}} b", "a | b"},
		{"escaped pipe inside arg", "{{co|red|x {{!}} y}}", "x | y"},
		{"escaped equals", "1 {{=}} 2", "1 = 2"},
		{"template parameter default", "{{{1|default}}}", "default"},
		{"template parameter without default", "x{{{name}}}y", "xy"},
		{"sentence", "Train {{sk|Gunnery|V}} and {{sk|Engineering}}.", "Train Gunnery V and Engineering."},
		{"multi-line template", "{{MessageBox|Title\n|Body line one\nbody line two}}", "Title Body line one body line two"},

		// --- chunk boundaries cut templates in half --------------------------------
		{"stray closer at chunk start", "tail of a long box}} then {{sk|Navigation|III}}", "tail of a long box then Navigation III"},
		{"unclosed opener at chunk end", "head {{MissionBriefing |We ran the hard drive through decryption", "head We ran the hard drive through decryption"},
		{"unclosed opener without pipe", "head {{Clear", "head Clear"},
		{"stray closers only", "}} }}", ""},

		// --- untouched text --------------------------------------------------------
		{"plain text", "Align to the warp-in point.  Keep   spacing.", "Align to the warp-in point.  Keep   spacing."},
		{"single braces", "[Rifter, PvP] {x} {{ }}", "[Rifter, PvP] {x}"},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, StripTemplates(tc.in))
		})
	}
}

func TestStripTemplatesIsIdempotent(t *testing.T) {
	in := "{{NPCTableHead|Wave 1}} {{NPCTableRow|Cruiser|2|Centum Execrator}} See {{sk|Gunnery|V}} {{co|red|!}} {{!}}"
	once := StripTemplates(in)
	require.NotContains(t, once, "{{")
	require.Equal(t, once, StripTemplates(once))
}

func TestStripTemplatesNoTemplateReturnsInputUnchanged(t *testing.T) {
	in := "  leading and   inner spaces stay  \n\nand newlines too"
	require.Equal(t, in, StripTemplates(in))
}

// A runaway chunk of braces must not loop or blow up.
func TestStripTemplatesPathological(t *testing.T) {
	in := "{{{{{{{{{{{{{{{{{{{{ a }}}}}}}}}}}}}}}}}}}}"
	require.NotPanics(t, func() { _ = StripTemplates(in) })
	require.NotContains(t, StripTemplates(in), "{{")
	require.NotContains(t, StripTemplates(in), "}}")
}

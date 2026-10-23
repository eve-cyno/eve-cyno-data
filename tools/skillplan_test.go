package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSkillPlan_BatchWithTrainingTime(t *testing.T) {
	text, plan := skillPlan(testSDE(t), skillPlanRequest{Items: []string{"Paladin", "Rifter"}, Purpose: "fly", TrainingTime: true})
	lines := strings.Split(text, "\n")
	require.Equal(t, "Skills to fly 2 items (training from zero SP: Omega, primary 27 / secondary 21 for every skill, its best remap, no implants):", lines[0])
	require.True(t, strings.HasPrefix(lines[1], "Paladin: 13 skills, 4,568,088 SP, 84d 14h 16m — Amarr Battleship V, Marauders I; chain: "), lines[1])
	require.Equal(t, "Rifter: 2 skills, 750 SP, 20m — Minmatar Frigate I; chain: Spaceship Command I", lines[2])
	require.Equal(t, "All 2 items: 14 skills, 4,568,588 SP, 84d 14h 29m", lines[3])
	require.Equal(t, 4568588, plan.Combined.SP)
	require.Equal(t, 7309741, plan.Combined.TrainingSeconds) // 4,568,588 SP / 37.5 SP per minute × 60, rounded
	require.Equal(t, 14, plan.Combined.Skills)
	require.True(t, plan.Attributes.BestRemapPerSkill)
	require.Nil(t, plan.Attributes.Perception)
	require.Len(t, plan.Items[0].Direct, 2)
	require.Len(t, plan.Items[0].Chain, 11)
	require.Equal(t, 13, plan.Items[0].Totals.Skills)
	for _, sl := range append(plan.Items[0].Direct, plan.Items[0].Chain...) {
		require.NotNil(t, sl.SP, sl.Name)
		require.NotNil(t, sl.TrainingSeconds, sl.Name)
	}
}

func TestSkillPlan_PinnedSingles(t *testing.T) {
	s := testSDE(t)
	text, plan := skillPlan(s, skillPlanRequest{Items: []string{"Damage Control II"}, TrainingTime: true})
	require.Contains(t, text, "Damage Control II: 2 skills, 90,760 SP, 1d 16h 20m — ")
	require.Contains(t, text, "Hull Upgrades IV")
	require.Contains(t, text, "Mechanics I")
	require.Nil(t, plan.Combined) // one item
	text, _ = skillPlan(s, skillPlanRequest{Items: []string{"Rifter"}, TrainingTime: true,
		Attributes: map[string]any{"perception": 20.0, "memory": 20.0, "willpower": 20.0, "intelligence": 20.0, "charisma": 19.0}})
	require.Contains(t, text, "Rifter: 2 skills, 750 SP, 25m")
}

func TestSkillPlan_BatchWithoutTrainingTimeHasNoNumbers(t *testing.T) {
	text, plan := skillPlan(testSDE(t), skillPlanRequest{Items: []string{"Rifter", "Paladin"}})
	lines := strings.Split(text, "\n")
	require.Equal(t, "Skills to fly 2 items:", lines[0])
	require.Equal(t, "Rifter: 2 skills — Minmatar Frigate I; chain: Spaceship Command I", lines[1])
	require.Len(t, lines, 3)
	require.Nil(t, plan.Attributes)
	require.Nil(t, plan.Combined)
	require.Nil(t, plan.Items[0].Totals)
	require.Nil(t, plan.Items[0].Direct[0].SP)
}

func TestSkillPlan_AttributesValidated(t *testing.T) {
	s := testSDE(t)
	fresh := map[string]any{"perception": 20.0, "memory": 20.0, "willpower": 20.0, "intelligence": 20.0, "charisma": 19.0}
	text, plan := skillPlan(s, skillPlanRequest{Items: []string{"Paladin"}, Purpose: "fly", TrainingTime: true, Attributes: fresh})
	require.Contains(t, text, "Paladin: 13 skills, 4,568,088 SP, 105d 17h 50m")
	require.Equal(t, 19, *plan.Attributes.Charisma)
	require.False(t, plan.Attributes.BestRemapPerSkill)
	text, plan = skillPlan(s, skillPlanRequest{Items: []string{"Paladin"}, TrainingTime: true, Attributes: map[string]any{"perception": 16.0}})
	require.Equal(t, "attributes.perception: 17 to 50 (got 16).", text)
	require.Nil(t, plan)
	text, _ = skillPlan(s, skillPlanRequest{Items: []string{"Paladin"}, TrainingTime: true, Attributes: map[string]any{"perception": 51.0, "memory": 20.0}})
	require.Equal(t, "attributes.perception: 17 to 50 (got 51).", text)
	text, _ = skillPlan(s, skillPlanRequest{Items: []string{"Paladin"}, TrainingTime: true, Attributes: map[string]any{"perseption": 20.0}})
	require.Equal(t, "Unknown attribute: perseption (known: charisma, intelligence, memory, perception, willpower).", text)
	text, _ = skillPlan(s, skillPlanRequest{Items: []string{"Paladin"}, TrainingTime: true, Attributes: map[string]any{"perception": 27.0}})
	require.Equal(t, "attributes: give all five (charisma, intelligence, memory, perception, willpower) or none.", text)
	text, _ = skillPlan(s, skillPlanRequest{Items: []string{"Paladin"}, TrainingTime: true, Attributes: map[string]any{"perception": 20.5}})
	require.Equal(t, "attributes.perception: a whole number 17 to 50.", text)
}

func TestSkillPlan_CapEmptyAndUnknown(t *testing.T) {
	s := testSDE(t)
	many := make([]string, 21)
	for i := range many {
		many[i] = "Rifter"
	}
	text, plan := skillPlan(s, skillPlanRequest{Items: many})
	require.Equal(t, "items: at most 20 per call (got 21); split the list.", text)
	require.Nil(t, plan)
	text, _ = skillPlan(s, skillPlanRequest{ItemName: "Rifter", Items: many[:20]}) // item_name counts too
	require.Equal(t, "items: at most 20 per call (got 21); split the list.", text)
	text, _ = skillPlan(s, skillPlanRequest{Items: many[:20]})
	require.NotContains(t, text, "at most")
	text, plan = skillPlan(s, skillPlanRequest{Items: []string{"rifter", "Rifter ", "Zzzzqq"}, Purpose: "fly"})
	require.Len(t, plan.Items, 1)
	require.Equal(t, []string{"Zzzzqq"}, plan.Unresolved)
	require.Contains(t, text, "Unknown items: Zzzzqq (resolve names with search_item_by_name).")
	text, plan = skillPlan(s, skillPlanRequest{Items: []string{"Zzzzqq"}})
	require.Equal(t, "Unknown items: Zzzzqq (resolve names with search_item_by_name).", text)
	require.Empty(t, plan.Items)
}

func TestSkillPlan_ItemNameIsPrependedAndBuildUsesBlueprintSkills(t *testing.T) {
	s := testSDE(t)
	text, plan := skillPlan(s, skillPlanRequest{ItemName: "Rifter", Items: []string{"Paladin"}})
	require.Equal(t, []string{"Rifter", "Paladin"}, []string{plan.Items[0].Name, plan.Items[1].Name})
	require.True(t, strings.HasPrefix(text, "Skills to fly 2 items:"), text)
	text, plan = skillPlan(s, skillPlanRequest{Items: []string{"Rifter"}, Purpose: "build", TrainingTime: true})
	require.Equal(t, "build", plan.Purpose)
	require.True(t, strings.HasPrefix(text, "Skills to build 1 item (training"), text)
	require.NotContains(t, text, "Minmatar Frigate") // the blueprint's skills, not the hull's
	require.Equal(t, "Rifter: 1 skill, 250 SP, 7m — Industry I", strings.Split(text, "\n")[1])
}

func TestSkillPlan_BuildItemNoBlueprintMakesIsUnresolvedWithReason(t *testing.T) {
	text, plan := skillPlan(testSDE(t), skillPlanRequest{Items: []string{"Rifter", "Tritanium"}, Purpose: "build", TrainingTime: true})
	lines := strings.Split(text, "\n")
	require.Equal(t, "Rifter: 1 skill, 250 SP, 7m — Industry I", lines[1])
	require.Equal(t, "Tritanium: no blueprint produces it.", lines[2])
	require.NotContains(t, text, "no skills found")
	require.NotContains(t, text, "Unknown items")
	require.Len(t, plan.Items, 1)
	require.Equal(t, []string{"Tritanium (no blueprint produces it)"}, plan.Unresolved)
}

func TestSkillPlan_BlankRequestIsAsked(t *testing.T) {
	want := "item_name or items: give one, e.g. item_name 'Rifter' or items ['Paladin', 'Rifter']."
	for _, args := range []map[string]any{{}, {"training_time": true}, {"item_name": "  ", "items": []any{}}} {
		out, err := ExecuteTool(context.Background(), testDeps(t), "get_required_skills", args)
		require.NoError(t, err)
		require.Equal(t, want, out)
	}
	res, err := ExecuteToolResult(context.Background(), testDeps(t), "get_required_skills", map[string]any{})
	require.NoError(t, err)
	require.Nil(t, res.Data)
}

func TestSkillPlan_AllNullAttributesCountAsAbsent(t *testing.T) {
	s := testSDE(t)
	base, _ := skillPlan(s, skillPlanRequest{Items: []string{"Rifter"}, TrainingTime: true})
	text, plan := skillPlan(s, skillPlanRequest{Items: []string{"Rifter"}, TrainingTime: true,
		Attributes: map[string]any{"perception": nil, "memory": nil, "willpower": nil, "intelligence": nil, "charisma": nil}})
	require.Equal(t, base, text)
	require.True(t, plan.Attributes.BestRemapPerSkill)
}

func TestSkillPlan_TrainingTextMinutesComeFromTheExactTotal(t *testing.T) {
	// The Gila totals 127,289.6 s: 2,121.49 minutes. Rounding to 127,290 s first would print 2,122.
	text, plan := skillPlan(testSDE(t), skillPlanRequest{Items: []string{"Gila"}, TrainingTime: true})
	require.Equal(t, 127290, plan.Items[0].Totals.TrainingSeconds)
	require.Contains(t, text, "1d 11h 21m")
	text, _ = skillPlan(testSDE(t), skillPlanRequest{ItemName: "Gila", TrainingTime: true})
	require.Contains(t, text, ", 1d 11h 21m.")
}

func TestSkillPlan_ChainRaisesADirectSkillLevel(t *testing.T) {
	// Mjolnir Cruise Missile needs Missile Launcher Operation I itself; Cruise Missiles needs it at V.
	s := testSDE(t)
	tid := s.ResolveNames([]string{"Mjolnir Cruise Missile"})["Mjolnir Cruise Missile"]
	var rawLevel int
	for _, d := range s.GetRequiredSkillsFromDogma(tid) {
		if d[0] == 3319 {
			rawLevel = d[1]
		}
	}
	require.Equal(t, 1, rawLevel)
	text, plan := skillPlan(s, skillPlanRequest{Items: []string{"Mjolnir Cruise Missile"}})
	got := map[string]int{}
	for _, sl := range plan.Items[0].Direct {
		got[sl.Name] = sl.Level
	}
	require.Equal(t, 5, got["Missile Launcher Operation"])
	require.Contains(t, text, "Missile Launcher Operation V")
	for _, sl := range plan.Items[0].Chain {
		require.NotEqual(t, "Missile Launcher Operation", sl.Name)
	}
}

func TestRequiredSkills_SingleWithTrainingTimeAppends(t *testing.T) {
	s := testSDE(t)
	base := golden(t, "tools/get_required_skills_rifter.txt")
	text, plan := skillPlan(s, skillPlanRequest{ItemName: "Rifter", Purpose: "fly", TrainingTime: true})
	require.Equal(t, base+"\n\nTraining from zero SP (Omega, primary 27 / secondary 21 for every skill, its best remap, no implants): 2 skills, 750 SP, 20m.", text)
	require.Equal(t, 750, plan.Items[0].Totals.SP)
	text, plan = skillPlan(s, skillPlanRequest{ItemName: "Rifter"})
	require.Equal(t, base, text)
	require.Nil(t, plan.Items[0].Totals)
}

func TestRequiredSkills_EmptyNewParamsKeepTodaysText(t *testing.T) {
	out, err := ExecuteTool(context.Background(), testDeps(t), "get_required_skills",
		map[string]any{"item_name": "Rifter", "items": []any{}, "training_time": nil, "attributes": nil})
	require.NoError(t, err)
	require.Equal(t, golden(t, "tools/get_required_skills_rifter.txt"), out)
	out, err = ExecuteTool(context.Background(), testDeps(t), "get_required_skills",
		map[string]any{"item_name": "Rifter", "items": "", "training_time": false, "attributes": map[string]any{}})
	require.NoError(t, err)
	require.Equal(t, golden(t, "tools/get_required_skills_rifter.txt"), out)
}

func TestRequiredSkills_DispatchBatchIsTyped(t *testing.T) {
	res, err := ExecuteToolResult(context.Background(), testDeps(t), "get_required_skills",
		map[string]any{"items": []any{"Paladin", "Rifter"}, "training_time": true})
	require.NoError(t, err)
	plan := requireDataIs[SkillPlan](t, res)
	require.Equal(t, 4568588, plan.Combined.SP)
}

func TestFmtTrainingTime(t *testing.T) {
	for sec, want := range map[float64]string{0: "0m", 29: "0m", 29.6: "0m", 30: "1m", 1200: "20m", 3600: "1h 0m", 5 * 3600: "5h 0m", 86400: "1d 0h 0m", 7309741: "84d 14h 29m"} {
		require.Equal(t, want, fmtTrainingTime(sec), sec)
	}
}

func TestSkillPlan_SchemaAttributeFloorMatchesTheCode(t *testing.T) {
	for _, entry := range ToolSchemas() {
		fn, _ := entry["function"].(map[string]any)
		if fn["name"] != "get_required_skills" {
			continue
		}
		props := fn["parameters"].(map[string]any)["properties"].(map[string]any)
		attrs := props["attributes"].(map[string]any)["properties"].(map[string]any)
		require.Len(t, attrs, 5)
		for name, raw := range attrs {
			p := raw.(map[string]any)
			require.Equal(t, float64(minAttribute), p["minimum"], name)
			require.Equal(t, float64(maxAttribute), p["maximum"], name)
		}
		_, hasRequired := fn["parameters"].(map[string]any)["required"]
		require.False(t, hasRequired, "item_name is optional beside items")
		return
	}
	t.Fatal("get_required_skills is not in the schema")
}

func TestSkillPlan_ChainLeavesOutDirectSkills(t *testing.T) {
	// The Avatar requires Capital Ships V itself and again through Titan: the skill is listed once, as direct.
	_, plan := skillPlan(testSDE(t), skillPlanRequest{Items: []string{"Avatar"}, TrainingTime: true})
	direct := map[string]int{}
	for _, sl := range plan.Items[0].Direct {
		direct[sl.Name] = sl.Level
	}
	require.Equal(t, 5, direct["Capital Ships"])
	for _, sl := range plan.Items[0].Chain {
		require.NotContains(t, direct, sl.Name)
	}
	require.Equal(t, len(plan.Items[0].Direct)+len(plan.Items[0].Chain), plan.Items[0].Totals.Skills)
}

func TestSkillPlan_LooseNamesSayWhatTheyResolvedTo(t *testing.T) {
	// "Rifter Zzzz" resolves loosely to the Rifter, as get_required_skills always has.
	s := testSDE(t)
	text, plan := skillPlan(s, skillPlanRequest{Items: []string{"Rifter Zzzz"}})
	require.True(t, strings.HasPrefix(strings.Split(text, "\n")[1], "Rifter Zzzz (as Rifter): 2 skills"), text)
	require.Equal(t, "Rifter", plan.Items[0].Name)
	text, plan = skillPlan(s, skillPlanRequest{Items: []string{"Rifter", "Rifter Zzzz"}})
	require.Len(t, plan.Items, 1, text)
	require.Empty(t, plan.Unresolved)
	require.Equal(t, "Rifter Zzzz: same as Rifter.", strings.Split(text, "\n")[2])
	text, _ = skillPlan(s, skillPlanRequest{Items: []string{"rifter", "Rifter "}}) // the same name twice is not noise worth a line
	require.Len(t, strings.Split(text, "\n"), 2, text)
}

package tools

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlanetaryChain_RoboticsAndCoolant(t *testing.T) {
	s := testSDE(t)
	text, chain, _ := getProductionChain(context.Background(), nil, s, productionRequest{Item: "Robotics", Runs: 1, ME: 10})
	require.True(t, strings.HasPrefix(text, "Planetary production chain for **Robotics** (1 run = 1 cycle of 3 units):"))
	for _, leaf := range []string{"Base Metals", "Noble Metals", "Heavy Metals", "Non-CS Crystals"} {
		require.Regexp(t, `(?m)^`+regexp.QuoteMeta(leaf)+` +12,000$`, text)
	}
	require.Equal(t, "planetary", chain.Kind)
	require.Nil(t, chain.MELevel)
	require.Len(t, chain.Steps, 7) // 1 P3 + 2 P2 + 4 P1
	text, _, _ = getProductionChain(context.Background(), nil, s, productionRequest{Item: "Robotics", Runs: 2, ME: 10})
	require.Regexp(t, `(?m)^Base Metals +24,000$`, text)
	text, _, _ = getProductionChain(context.Background(), nil, s, productionRequest{Item: "Coolant", Runs: 1, ME: 10})
	require.Regexp(t, `(?m)^Ionic Solutions +6,000$`, text)
	require.Regexp(t, `(?m)^Aqueous Liquids +6,000$`, text)
	text, chain, _ = getProductionChain(context.Background(), nil, s, productionRequest{Item: "Tritanium", Runs: 1, ME: 10})
	require.Equal(t, "No blueprint found that produces 'Tritanium' (typeID=34).", text)
	require.Nil(t, chain)
}

func TestPlanetaryChain_StepsAreOrderedAndExact(t *testing.T) {
	s := testSDE(t)
	text, chain, err := getProductionChain(context.Background(), nil, s, productionRequest{Item: "Robotics", Runs: 1, ME: 10})
	require.NoError(t, err)
	require.Regexp(t, `(?m)^P3  Robotics +3 per 1h +1 cycle   from 10 Mechanical Parts, 10 Consumer Electronics$`, text)
	require.Regexp(t, `(?m)^P2  Mechanical Parts +5 per 1h +2 cycles  from 40 Reactive Metals, 40 Precious Metals$`, text)
	require.Regexp(t, `(?m)^P1  Reactive Metals +20 per 30m +4 cycles  from 3,000 Base Metals$`, text)
	require.True(t, strings.HasSuffix(text, "Note: ME, rigs and cost indices do not apply to planetary production."))
	var order []string
	for _, st := range chain.Steps {
		order = append(order, st.Tier+" "+st.Name)
		require.Equal(t, 97 == st.SchematicID, st.Tier == "P3")
	}
	require.Equal(t, []string{"P3 Robotics", "P2 Consumer Electronics", "P2 Mechanical Parts",
		"P1 Chiral Structures", "P1 Precious Metals", "P1 Reactive Metals", "P1 Toxic Metals"}, order)
	require.Len(t, chain.Materials, 4)
	require.Equal(t, 12000, chain.Materials[0].Quantity)
	require.Nil(t, chain.Cost)
}

func TestPlanetaryChain_SystemReadsNothingAndSaysSo(t *testing.T) {
	s := testSDE(t)
	c, fake := newFakeESIClient(t, industryHandler(industryPricesBody, industrySystemsBody, 200))
	text, chain, err := getProductionChain(context.Background(), c, s, productionRequest{Item: "Robotics", Runs: 1, ME: 10, System: "Jita"})
	require.NoError(t, err)
	require.Contains(t, text, "system: cost indices apply to manufacturing jobs, not planetary production.")
	require.Empty(t, fake.requests())
	require.Equal(t, "planetary", chain.Kind)
}

func TestPlanetaryChain_IncludeCostValuesTheRawResources(t *testing.T) {
	s := testSDE(t)
	body := `[{"type_id":2267,"adjusted_price":1,"average_price":2},{"type_id":2268,"adjusted_price":1,"average_price":3}]`
	c, _ := newFakeESIClient(t, industryHandler(body, industrySystemsBody, 200))
	text, chain, err := getProductionChain(context.Background(), c, s, productionRequest{Item: "Coolant", Runs: 1, ME: 10, IncludeCost: true})
	require.NoError(t, err)
	require.Contains(t, text, "Raw resources (ESI average prices of ")
	require.NotNil(t, chain.Cost)
	require.Nil(t, chain.Cost.JobFee)
	require.NotEmpty(t, chain.Cost.Unpriced, "materials without an average price are named, not valued at 0")
	require.Nil(t, chain.Cost.Total)
}

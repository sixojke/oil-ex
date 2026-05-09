package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"
)

// ── Структуры common.json ────────────────────────────────────────

type FluidIO struct {
	Fluid  string `json:"fluid"`
	Amount int    `json:"amount"`
}

type RefineryRecipe struct {
	ID         string    `json:"id"`
	EnergyCost int       `json:"energy_cost"`
	CycleTick  int       `json:"cycle_tick"`
	Input      []FluidIO `json:"input"`
	Output     []FluidIO `json:"output"`
}

type OilRefinery struct {
	Tank          int              `json:"tank"`
	EnergyStorage int              `json:"energy_storage"`
	Recipes       []RefineryRecipe `json:"recipes"`
}

type OilDerrick struct {
	EnergyStorage int `json:"energy_storage"`
	Tank          int `json:"tank"`
	CycleTick     int `json:"cycle_tick"`
	EnergyCost    int `json:"energy_cost"`
}

type Fuel struct {
	Amount int `json:"amount"`
	Energy int `json:"energy"`
}

type OilGenerator struct {
	Tank                   int             `json:"tank"`
	MaxOutputEU            int             `json:"max_output_eu"`
	CycleTick              int             `json:"cycle_tick"`
	UpgradeBoost           int             `json:"upgrade_boost"`
	UpgradeDurabilityCycle int             `json:"upgrade_durability_cycle"`
	Fuels                  map[string]Fuel `json:"fuels"`
}

type UltAssembler struct {
	Tank              int     `json:"tank"`
	UpgradeLiquid     string  `json:"upgrade_liquid"`
	UpgradeBoostCycle int     `json:"upgrade_boost_cycle"`
	UpgradeBoostEU    int     `json:"upgrade_boost_eu"`
	UpgradePriceTicks float64 `json:"upgrade_price_ticks"`
}

type Machines struct {
	OilDerrick   OilDerrick   `json:"oil_derrick"`
	OilRefinery  OilRefinery  `json:"oil_refinery"`
	OilGenerator OilGenerator `json:"oil_generator"`
	UltAssembler UltAssembler `json:"ult_assembler"`
}

type CrudeOil struct {
	OilChunkChance   float64 `json:"oil_chunk_chance"`
	ChunkFluidGenMin int     `json:"chunk_fluid_gen_min"`
	ChunkFluidGenMax int     `json:"chunk_fluid_gen_max"`
}

type Base struct {
	CrudeOil CrudeOil `json:"crude_oil"`
}

type Config struct {
	Base    Base     `json:"base"`
	Machine Machines `json:"machine"`
}

const SamplePoints = 5

// ── Структуры analyze.json ───────────────────────────────────────

type AnalyzeSetup struct {
	MaxOilDerricks        int     `json:"max_oil_derricks"`
	LimitRefineries       float64 `json:"limit_refineries"`
	LimitFuelGenerators   float64 `json:"limit_fuel_generators"`
	TurbineSealDurability int     `json:"turbine_seal_durability"`
	MatterEUPerMB         int     `json:"matter_eu_per_mb"`
}

// ZoneRange — 4-зонный порог: красный/жёлтый/зелёный/жёлтый/красный
// (ниже red_low → красный, [red_low..yellow_low) → жёлтый,
// [yellow_low..yellow_high] → зелёный, (yellow_high..red_high] → жёлтый,
// выше red_high → красный).
type ZoneRange struct {
	RedLow     float64 `json:"red_low"`
	YellowLow  float64 `json:"yellow_low"`
	YellowHigh float64 `json:"yellow_high"`
	RedHigh    float64 `json:"red_high"`
}

// ZoneBelow — односторонний порог «больше = лучше»: ниже red_below → красный,
// ниже yellow_below → жёлтый, выше → зелёный.
type ZoneBelow struct {
	RedBelow    float64 `json:"red_below"`
	YellowBelow float64 `json:"yellow_below"`
}

// ZoneAbove — односторонний порог «меньше = лучше»: выше red_above → красный,
// выше yellow_above → жёлтый, ниже → зелёный.
type ZoneAbove struct {
	YellowAbove float64 `json:"yellow_above"`
	RedAbove    float64 `json:"red_above"`
}

// ZoneFactor — симметричное отклонение от лимита по множителю:
// |ratio| ≥ red_factor (или ≤ 1/red_factor) → красный, аналогично для yellow.
type ZoneFactor struct {
	YellowFactor float64 `json:"yellow_factor"`
	RedFactor    float64 `json:"red_factor"`
}

type Thresholds struct {
	OilChunksPerRegion ZoneRange  `json:"oil_chunks_per_region"`
	LubricantRatio     ZoneRange  `json:"lubricant_ratio"`
	FluidRemainder     ZoneRange  `json:"fluid_remainder"`
	StorageRatio       ZoneBelow  `json:"storage_ratio"`
	SealsPerHour       ZoneAbove  `json:"seals_per_hour"`
	LimitRatio         ZoneFactor `json:"limit_ratio"`
}

type AnalyzeConfig struct {
	Setup      AnalyzeSetup `json:"setup"`
	Thresholds Thresholds   `json:"thresholds"`
}

// thresholds — глобальный пакетный кеш, заполняется в main() из analyze.json,
// чтобы color-функции не пробрасывали AnalyzeConfig сквозь всю цепочку.
var thresholds Thresholds

// ── Локальный тип для расчёта по топливу ─────────────────────────

type FuelInfo struct {
	Name      string
	Available float64 // mB/t — поток с НПЗ
	PerGen    float64 // mB/t — расход на 1 ген
	BaseEU    float64 // EU/t — мощность 1 гена базовая
	SealEU    float64 // EU/t — мощность 1 гена с уплотнителем (clamped)
	Gens      int     // активных генов на этом топливе
}

// ── main ─────────────────────────────────────────────────────────

func main() {
	cfg, err := loadConfig("common.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка загрузки common.json: %v\n", err)
		os.Exit(1)
	}
	ac, err := loadAnalyze("analyze.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ошибка загрузки analyze.json: %v\n", err)
		os.Exit(1)
	}
	thresholds = ac.Thresholds

	rates := evenlyDistributed(
		cfg.Base.CrudeOil.ChunkFluidGenMin,
		cfg.Base.CrudeOil.ChunkFluidGenMax,
		SamplePoints,
	)

	header(cfg, ac)

	// Группировка по количеству вышек (1..max), внутри — по топливам
	for derricks := 1; derricks <= ac.Setup.MaxOilDerricks; derricks++ {
		fmt.Printf("══════════════ %d %s ══════════════\n",
			derricks, derrickPhrase(derricks))

		for _, fuelName := range sortedFuelNames(cfg.Machine.OilGenerator.Fuels) {
			fmt.Printf("── Топливо \"%s\" ─────\n", fuelName)

			t := table.NewWriter()
			t.SetOutputMirror(os.Stdout)
			t.SetStyle(table.StyleLight)
			t.Style().Format.Header = text.FormatDefault

			t.AppendHeader(table.Row{
				"mB/cycle", "mB/t/выш", "сумма",
				"загруж.НПЗ", "загруж.гены",
				"выход база", "выход +упл",
				"затрат",
				"профит база", "профит +упл",
				"КПД база", "КПД +упл",
			})

			cfgs := make([]table.ColumnConfig, 12)
			for i := range cfgs {
				cfgs[i] = table.ColumnConfig{Number: i + 1, Align: text.AlignRight}
			}
			t.SetColumnConfigs(cfgs)

			for _, rate := range rates {
				analyzeFuel(rate, cfg, derricks, fuelName,
					ac.Setup.LimitRefineries, ac.Setup.LimitFuelGenerators, t)
			}

			t.Render()
			fmt.Println()
		}
	}

	printChunkAnalysis(cfg)
	printEnergyStorageCheck(cfg)
	printFluidBalance(cfg, ac)
	printLubricantAnalysis(cfg)
	printSealConsumption(cfg, ac)

	printMatterProduction(cfg, ac)

	fmt.Print("\nНажмите Enter для выхода...")
	fmt.Scanln()
}

// ANSI цвета (сырые escape-коды — работают всегда, даже когда вывод не в TTY)
const (
	ansiReset  = "\x1b[0m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
)

// printSealConsumption считает расход турбинных уплотнителей на limit_fuel_generators
// генов с буст-режимом. На 1 ген за каждый цикл (cycle_tick) теряется
// upgrade_durability_cycle единиц прочности. При прочности уплотнителя
// turbine_seal_durability получаем шт/час на полном лимите генов.
//   - ≤ 30 шт/час  → зелёный (управляемый расход)
//   - 30..120     → жёлтый (1-2 уплотн. в минуту)
//   - > 120       → красный (надо целое производство уплотнителей)
func printSealConsumption(cfg Config, ac AnalyzeConfig) {
	g := cfg.Machine.OilGenerator
	if g.UpgradeDurabilityCycle <= 0 || g.CycleTick <= 0 {
		return
	}
	dura := ac.Setup.TurbineSealDurability
	gens := ac.Setup.LimitFuelGenerators

	duraPerTick := float64(g.UpgradeDurabilityCycle) / float64(g.CycleTick)
	duraPerGenSec := duraPerTick * 20
	duraPerGenHour := duraPerGenSec * 3600

	sealLifeSec := 0.0
	if duraPerGenSec > 0 && dura > 0 {
		sealLifeSec = float64(dura) / duraPerGenSec
	}

	collectiveDuraHour := duraPerGenHour * gens
	sealsPerHour := 0.0
	if dura > 0 {
		sealsPerHour = collectiveDuraHour / float64(dura)
	}

	fmt.Println("══════════════ Расход турбинных уплотнителей ══════════════")
	fmt.Printf("1 ген с буст: %d ед. прочности / %dt = %.2f ед./сек = %.0f ед./час\n",
		g.UpgradeDurabilityCycle, g.CycleTick, duraPerGenSec, duraPerGenHour)
	fmt.Printf("Прочность уплотнителя: %d → 1 уплотн. на 1 гене живёт %s\n",
		dura, formatTime(sealLifeSec))
	fmt.Printf("На %.0f генах коллективно: %.0f ед./час → %s уплотнителей/час (1 шт каждые %s)\n",
		gens, collectiveDuraHour,
		colorBySealsPerHour(sealsPerHour, fmt.Sprintf("%.2f", sealsPerHour)),
		formatTime(3600.0/sealsPerHour))
	fmt.Println()
}

func colorBySealsPerHour(v float64, raw string) string {
	return colorByZoneAbove(v, thresholds.SealsPerHour, raw)
}

// printMatterProduction оценивает выход UU-материи на limit_fuel_generators
// генераторах при разных топливе/режимах. Цена 1 mB материи берётся из
// analyze.json (matter_eu_per_mb).
func printMatterProduction(cfg Config, ac AnalyzeConfig) {
	if ac.Setup.MatterEUPerMB <= 0 {
		return
	}
	g := cfg.Machine.OilGenerator
	gens := ac.Setup.LimitFuelGenerators

	fmt.Println("══════════════ Производство UU-материи ══════════════")
	fmt.Printf("Цена 1 mB материи: %d EU; в сборке %.0f генов\n",
		ac.Setup.MatterEUPerMB, gens)
	fmt.Println()

	t := table.NewWriter()
	t.SetOutputMirror(os.Stdout)
	t.SetStyle(table.StyleLight)
	t.Style().Format.Header = text.FormatDefault
	t.AppendHeader(table.Row{
		"топливо", "режим", "EU/t всего", "mB/мин", "mB/час",
	})
	cfgs := make([]table.ColumnConfig, 5)
	for i := range cfgs {
		cfgs[i] = table.ColumnConfig{Number: i + 1, Align: text.AlignRight}
	}
	cfgs[0].Align = text.AlignLeft
	cfgs[1].Align = text.AlignLeft
	t.SetColumnConfigs(cfgs)

	for _, name := range sortedFuelNames(g.Fuels) {
		fuel := g.Fuels[name]
		baseEU := float64(fuel.Energy) / float64(g.CycleTick)
		sealEU := baseEU * (1.0 + float64(g.UpgradeBoost)/100.0)
		if baseEU > float64(g.MaxOutputEU) {
			baseEU = float64(g.MaxOutputEU)
		}
		if sealEU > float64(g.MaxOutputEU) {
			sealEU = float64(g.MaxOutputEU)
		}

		for _, m := range []struct {
			label string
			eu    float64
		}{{"база", baseEU}, {"+упл", sealEU}} {
			totalEU := m.eu * gens
			euPerSec := totalEU * 20
			mPerMin := euPerSec * 60 / float64(ac.Setup.MatterEUPerMB)
			mPerHour := mPerMin * 60
			t.AppendRow(table.Row{
				name, m.label,
				fmt.Sprintf("%.0f", totalEU),
				fmt.Sprintf("%.1f", mPerMin),
				fmt.Sprintf("%.0f", mPerHour),
			})
		}
	}
	t.Render()
	fmt.Println()
}

// printFluidBalance показывает потоки жидкостей при максимальной добыче и
// заданной сборке (limit_refineries НПЗ-фракции + 1 НПЗ-смазка) с учётом
// потребления limit_fuel_generators генераторов.
// Колонки fuel/bitumen/gasoline — нетто после расхода смазкой и генераторами,
// то есть «что остаётся на крафты».
func printFluidBalance(cfg Config, ac AnalyzeConfig) {
	fractions := findRecipe(cfg.Machine.OilRefinery.Recipes, "craft_fractions")
	if fractions.CycleTick == 0 {
		return
	}

	g := cfg.Machine.OilGenerator
	npzFractions := ac.Setup.LimitRefineries
	derricks := float64(ac.Setup.MaxOilDerricks)
	maxChunk := cfg.Base.CrudeOil.ChunkFluidGenMax
	derrickCT := cfg.Machine.OilDerrick.CycleTick

	derrickFlow := float64(maxChunk) / float64(derrickCT) * derricks
	crudeIn := float64(findIO(fractions.Input, "crude_oil"))
	npzCapacityCrude := crudeIn / float64(fractions.CycleTick) * npzFractions

	fracFlow := derrickFlow
	if fracFlow > npzCapacityCrude {
		fracFlow = npzCapacityCrude
	}

	yieldG := float64(findIO(fractions.Output, "gasoline")) / crudeIn
	yieldF := float64(findIO(fractions.Output, "fuel_oil")) / crudeIn
	yieldB := float64(findIO(fractions.Output, "bitumen")) / crudeIn

	gasOut := fracFlow * yieldG
	fuelOut := fracFlow * yieldF
	bitOut := fracFlow * yieldB

	// Выбираем для генераторов топливо с большим EU/mB (наиболее эффективное)
	genFuelName := ""
	bestEUperMB := 0.0
	for name, fuel := range g.Fuels {
		if fuel.Amount <= 0 {
			continue
		}
		eumb := float64(fuel.Energy) / float64(fuel.Amount)
		if eumb > bestEUperMB {
			bestEUperMB = eumb
			genFuelName = name
		}
	}
	gens := ac.Setup.LimitFuelGenerators
	genConsumeMin := 0.0
	if genFuelName != "" && g.CycleTick > 0 {
		genConsumeMin = gens * float64(g.Fuels[genFuelName].Amount) / float64(g.CycleTick) * 1200
	}
	genGasMin := 0.0
	genFuelOilMin := 0.0
	if genFuelName == "gasoline" {
		genGasMin = genConsumeMin
	} else if genFuelName == "fuel_oil" {
		genFuelOilMin = genConsumeMin
	}

	fmt.Println("══════════════ Жидкости в минуту (худший случай) ══════════════")
	fmt.Printf("Вышки (%.0f × %d mB/cycle / %dt):     crude_oil  +%.0f mB/мин\n",
		derricks, maxChunk, derrickCT, derrickFlow*1200)
	fmt.Printf("%.0f НПЗ-фракции потребляют:           crude_oil  -%.0f mB/мин (макс. %.0f mB/мин)\n",
		npzFractions, fracFlow*1200, npzCapacityCrude*1200)
	crudeLeftover := (derrickFlow - fracFlow) * 1200
	if crudeLeftover > 0 {
		fmt.Printf("  ⚠ остаток crude_oil:                +%.0f mB/мин (буфер вышек заполнится)\n",
			crudeLeftover)
	} else {
		fmt.Printf("  остаток crude_oil:                   0 mB/мин (НПЗ переваривают всё)\n")
	}
	fmt.Printf("%.0f НПЗ-фракции выдают:                gasoline +%.0f / fuel_oil +%.0f / bitumen +%.0f mB/мин\n",
		npzFractions, gasOut*1200, fuelOut*1200, bitOut*1200)
	fmt.Printf("%.0f генераторов (на %s, %.0f EU/mB): -%.0f mB/мин %s\n",
		gens, genFuelName, bestEUperMB, genConsumeMin, genFuelName)
	fmt.Println()
	fmt.Println("Остатки на крафты после смазки и генераторов:")

	t := table.NewWriter()
	t.SetOutputMirror(os.Stdout)
	t.SetStyle(table.StyleLight)
	t.Style().Format.Header = text.FormatDefault
	t.AppendHeader(table.Row{
		"рецепт смазки",
		"gasoline ост.", "fuel_oil ост.", "bitumen ост.",
	})
	cfgs := make([]table.ColumnConfig, 4)
	for i := range cfgs {
		cfgs[i] = table.ColumnConfig{Number: i + 1, Align: text.AlignRight}
	}
	cfgs[0].Align = text.AlignLeft
	t.SetColumnConfigs(cfgs)

	gasBase := gasOut*1200 - genGasMin
	fuelBase := fuelOut*1200 - genFuelOilMin
	bitBase := bitOut * 1200

	addBalanceRow(t, "без смазки", gasBase, fuelBase, bitBase)

	for _, r := range cfg.Machine.OilRefinery.Recipes {
		if !strings.Contains(r.ID, "lubricant") {
			continue
		}
		ct := float64(r.CycleTick)
		dGas := (-float64(findIO(r.Input, "gasoline")) + float64(findIO(r.Output, "gasoline"))) / ct * 1200
		dFuel := (-float64(findIO(r.Input, "fuel_oil")) + float64(findIO(r.Output, "fuel_oil"))) / ct * 1200
		dBit := (-float64(findIO(r.Input, "bitumen")) + float64(findIO(r.Output, "bitumen"))) / ct * 1200
		addBalanceRow(t, r.ID, gasBase+dGas, fuelBase+dFuel, bitBase+dBit)
	}

	t.Render()
	fmt.Println()
}

// addBalanceRow принимает значения в mB/мин.
func addBalanceRow(t table.Writer, scenario string, gas, fuel, bit float64) {
	t.AppendRow(table.Row{
		scenario,
		colorByFluidRemainder(gas, fmt.Sprintf("%.0f", gas)),
		colorByFluidRemainder(fuel, fmt.Sprintf("%.0f", fuel)),
		colorByFluidRemainder(bit, fmt.Sprintf("%.0f", bit)),
	})
}

// colorByFluidRemainder раскрашивает остаток жидкости (mB/мин) по порогам
// thresholds.FluidRemainder (см. analyze.json).
func colorByFluidRemainder(v float64, raw string) string {
	return colorByZoneRange(v, thresholds.FluidRemainder, raw)
}

// printEnergyStorageCheck проверяет, что у каждой машины energy_storage достаточен
// для одного цикла (energy_storage ≥ energy_cost). Иначе машина не сможет накопить
// нужную энергию даже на один цикл и встанет.
//   - ratio ≥ 2.0 → зелёный (с запасом)
//   - 1.0..2.0    → жёлтый (впритык)
//   - < 1.0        → красный (поломано: не хватит даже на цикл)
func printEnergyStorageCheck(cfg Config) {
	fmt.Println("══════════════ Проверка energy_storage vs energy_cost ══════════════")

	t := table.NewWriter()
	t.SetOutputMirror(os.Stdout)
	t.SetStyle(table.StyleLight)
	t.Style().Format.Header = text.FormatDefault
	t.AppendHeader(table.Row{
		"машина", "цикл", "energy_storage", "energy_cost/cycle", "ratio",
	})
	cfgs := make([]table.ColumnConfig, 5)
	for i := range cfgs {
		cfgs[i] = table.ColumnConfig{Number: i + 1, Align: text.AlignRight}
	}
	cfgs[0].Align = text.AlignLeft
	cfgs[1].Align = text.AlignLeft
	t.SetColumnConfigs(cfgs)

	d := cfg.Machine.OilDerrick
	appendStorageRow(t, "oil_derrick", "—", d.EnergyStorage, d.EnergyCost)

	r := cfg.Machine.OilRefinery
	for _, rec := range r.Recipes {
		appendStorageRow(t, "oil_refinery", rec.ID, r.EnergyStorage, rec.EnergyCost)
	}

	t.Render()
	fmt.Println()
}

func appendStorageRow(t table.Writer, machine, cycle string, storage, cost int) {
	ratio := 0.0
	if cost > 0 {
		ratio = float64(storage) / float64(cost)
	}
	t.AppendRow(table.Row{
		machine, cycle, storage, cost,
		colorByStorageRatio(ratio, fmt.Sprintf("%.2f", ratio)),
	})
}

func colorByStorageRatio(ratio float64, raw string) string {
	return colorByZoneBelow(ratio, thresholds.StorageRatio, raw)
}

// Регион 128×128 блоков = 8×8 чанков по 16×16
const ChunksPerRegion = 64

// printChunkAnalysis выводит оценку плотности нефтяных чанков в регионе
func printChunkAnalysis(cfg Config) {
	chance := cfg.Base.CrudeOil.OilChunkChance
	expected := float64(ChunksPerRegion) * chance

	verdict := ""
	switch {
	case expected < 0.4:
		verdict = "🔴 нефти почти нет"
	case expected < 0.8:
		verdict = "🟡 нефти маловато"
	case expected <= 2.0:
		verdict = "🟢 нормальная плотность"
	case expected <= 4.0:
		verdict = "🟡 нефти многовато"
	default:
		verdict = "🔴 нефти слишком много"
	}

	fmt.Println("══════════════ Чанки нефти на регион ══════════════")
	fmt.Printf("Регион:   128×128 блоков (8×8 = %d чанков)\n", ChunksPerRegion)
	fmt.Printf("Чанк:     16×16 блоков\n")
	fmt.Printf("oil_chunk_chance: %.4f (%.2f%%)\n", chance, chance*100)
	fmt.Printf("Ожидаемых чанков с нефтью на регион: %s — %s\n",
		colorByOilChunks(expected, fmt.Sprintf("%.2f", expected)), verdict)
	fmt.Println()
}

// colorByOilChunks раскрашивает ожидаемое количество нефтяных чанков на регион
// по порогам thresholds.OilChunksPerRegion (см. analyze.json).
func colorByOilChunks(expected float64, raw string) string {
	return colorByZoneRange(expected, thresholds.OilChunksPerRegion, raw)
}

// colorByZoneRange — общий helper для 4-зонного порога (см. ZoneRange).
func colorByZoneRange(v float64, z ZoneRange, raw string) string {
	switch {
	case v < z.RedLow || v > z.RedHigh:
		return ansiRed + raw + ansiReset
	case v < z.YellowLow || v > z.YellowHigh:
		return ansiYellow + raw + ansiReset
	default:
		return ansiGreen + raw + ansiReset
	}
}

// colorByZoneBelow — больше = лучше (см. ZoneBelow).
func colorByZoneBelow(v float64, z ZoneBelow, raw string) string {
	switch {
	case v < z.RedBelow:
		return ansiRed + raw + ansiReset
	case v < z.YellowBelow:
		return ansiYellow + raw + ansiReset
	default:
		return ansiGreen + raw + ansiReset
	}
}

// colorByZoneAbove — меньше = лучше (см. ZoneAbove).
func colorByZoneAbove(v float64, z ZoneAbove, raw string) string {
	switch {
	case v > z.RedAbove:
		return ansiRed + raw + ansiReset
	case v > z.YellowAbove:
		return ansiYellow + raw + ansiReset
	default:
		return ansiGreen + raw + ansiReset
	}
}

// colorByZoneFactor — симметричное отклонение ratio=value/limit от единицы.
func colorByZoneFactor(value, limit float64, z ZoneFactor, raw string) string {
	if limit <= 0 || z.RedFactor <= 0 || z.YellowFactor <= 0 {
		return raw
	}
	ratio := value / limit
	switch {
	case ratio >= z.RedFactor || ratio <= 1.0/z.RedFactor:
		return ansiRed + raw + ansiReset
	case ratio >= z.YellowFactor || ratio <= 1.0/z.YellowFactor:
		return ansiYellow + raw + ansiReset
	default:
		return ansiGreen + raw + ansiReset
	}
}

// printLubricantAnalysis выводит анализ выработки смазки.
// Под смазку выделяется 1 НПЗ (планово 2 НПЗ под фракции/генераторы).
// Расход смазки в сборщике: upgrade_price_ticks mB за 1 игровой тик активного
// ускоренного крафта (т.е. при 12.5 → 250 mB/сек = 15000 mB/мин).
func printLubricantAnalysis(cfg Config) {
	a := cfg.Machine.UltAssembler

	consumePerTick := a.UpgradePriceTicks
	consumePerSec := consumePerTick * 20
	consumePerMin := consumePerSec * 60
	tankRuntimeSec := 0.0
	if consumePerSec > 0 {
		tankRuntimeSec = float64(a.Tank) / consumePerSec
	}

	fmt.Println("══════════════ Анализ выработки смазки ══════════════")
	fmt.Println("Под смазку выделен 1 НПЗ (планово 2 НПЗ под фракции/генераторы).")
	fmt.Printf("Сборщик: бак %d mB, upgrade_price_ticks=%.1f → расход %.1f mB/t = %.0f mB/сек = %.0f mB/мин\n",
		a.Tank, a.UpgradePriceTicks, consumePerTick, consumePerSec, consumePerMin)
	fmt.Printf("Полный бак сборщика при непрерывной нагрузке: %s\n", formatTime(tankRuntimeSec))
	fmt.Println()

	t := table.NewWriter()
	t.SetOutputMirror(os.Stdout)
	t.SetStyle(table.StyleLight)
	t.Style().Format.Header = text.FormatDefault
	t.AppendHeader(table.Row{
		"рецепт", "вход", "смазка/cycle", "1 НПЗ mB/мин",
		"сборщ./НПЗ", "налив бака",
	})
	cfgs := make([]table.ColumnConfig, 6)
	for i := range cfgs {
		cfgs[i] = table.ColumnConfig{Number: i + 1, Align: text.AlignRight}
	}
	cfgs[0].Align = text.AlignLeft
	cfgs[1].Align = text.AlignLeft
	t.SetColumnConfigs(cfgs)

	for _, r := range cfg.Machine.OilRefinery.Recipes {
		if !strings.Contains(r.ID, "lubricant") {
			continue
		}
		lubOut := findIO(r.Output, "lubricant")
		if lubOut == 0 {
			continue
		}

		lubPerTick := float64(lubOut) / float64(r.CycleTick)
		lubPerMin := lubPerTick * 20 * 60

		sustainedAssemblers := 0.0
		if consumePerMin > 0 {
			sustainedAssemblers = lubPerMin / consumePerMin
		}

		fillTimeSec := 0.0
		if lubPerTick > 0 {
			fillTimeSec = float64(a.Tank) / (lubPerTick * 20)
		}

		inputs := make([]string, 0, len(r.Input))
		for _, in := range r.Input {
			inputs = append(inputs, fmt.Sprintf("%d %s", in.Amount, in.Fluid))
		}

		t.AppendRow(table.Row{
			r.ID,
			strings.Join(inputs, " + "),
			fmt.Sprintf("%d / %dt", lubOut, r.CycleTick),
			fmt.Sprintf("%.0f", lubPerMin),
			colorByLubricantRatio(sustainedAssemblers, fmt.Sprintf("%.2f", sustainedAssemblers)),
			formatTime(fillTimeSec),
		})
	}

	t.Render()
	fmt.Println()
}

// colorByLubricantRatio раскрашивает отношение «сборщиков на 1 НПЗ»
// по порогам thresholds.LubricantRatio (см. analyze.json).
func colorByLubricantRatio(ratio float64, raw string) string {
	return colorByZoneRange(ratio, thresholds.LubricantRatio, raw)
}

// formatTime — компактное представление длительности: секунды / минуты / часы
func formatTime(sec float64) string {
	switch {
	case sec < 60:
		return fmt.Sprintf("%.0fс", sec)
	case sec < 3600:
		return fmt.Sprintf("%.1fмин", sec/60)
	default:
		return fmt.Sprintf("%.1fч", sec/3600)
	}
}

// colorByLimit раскрашивает по симметричному отклонению ratio=value/limit
// согласно thresholds.LimitRatio (см. analyze.json).
func colorByLimit(value, limit float64, raw string) string {
	return colorByZoneFactor(value, limit, thresholds.LimitRatio, raw)
}

// derrickPhrase возвращает правильную форму "нефтяная(ых) вышка(и/ек)" по числу
func derrickPhrase(n int) string {
	if n%10 == 1 && n%100 != 11 {
		return "нефтяная вышка"
	}
	if (n%10 >= 2 && n%10 <= 4) && (n%100 < 10 || n%100 >= 20) {
		return "нефтяных вышки"
	}
	return "нефтяных вышек"
}

func header(cfg Config, ac AnalyzeConfig) {
	d := cfg.Machine.OilDerrick
	r := findRecipe(cfg.Machine.OilRefinery.Recipes, "craft_fractions")
	g := cfg.Machine.OilGenerator

	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Println("  АНАЛИЗ ЭНЕРГЕТИКИ — обзор по выработке вышки")
	fmt.Println("══════════════════════════════════════════════════════════════════")
	fmt.Printf("Сборка: %d нефтяных вышек (НПЗ и генераторы масштабируются по необходимости)\n",
		ac.Setup.MaxOilDerricks)
	fmt.Printf("Точки: %d равномерных в диапазоне [%d..%d] mB/cycle (из chunk_fluid_gen_min/max)\n\n",
		SamplePoints, cfg.Base.CrudeOil.ChunkFluidGenMin, cfg.Base.CrudeOil.ChunkFluidGenMax)

	fmt.Printf("Из common.json:\n")
	fmt.Printf("  Вышка    : %d EU/cycle / %dt = %.1f EU/t\n",
		d.EnergyCost, d.CycleTick, float64(d.EnergyCost)/float64(d.CycleTick))
	fmt.Printf("  НПЗ-фрак : %d EU/cycle / %dt = %.1f EU/t\n",
		r.EnergyCost, r.CycleTick, float64(r.EnergyCost)/float64(r.CycleTick))
	fmt.Printf("  НПЗ нефти: %d mB/cycle = %.2f mB/t\n",
		findIO(r.Input, "crude_oil"), float64(findIO(r.Input, "crude_oil"))/float64(r.CycleTick))

	// Все горючие выходы НПЗ, которые ген может жечь
	crudeIn := findIO(r.Input, "crude_oil")
	fuelNames := sortedFuelNames(g.Fuels)
	fmt.Printf("  Топлива генератора:\n")
	for _, name := range fuelNames {
		fuel := g.Fuels[name]
		base := float64(fuel.Energy) / float64(g.CycleTick)
		seal := base * (1.0 + float64(g.UpgradeBoost)/100.0)
		if seal > float64(g.MaxOutputEU) {
			seal = float64(g.MaxOutputEU)
		}
		if base > float64(g.MaxOutputEU) {
			base = float64(g.MaxOutputEU)
		}
		yieldFromCrude := 0
		if crudeIn > 0 {
			yieldFromCrude = findIO(r.Output, name)
		}
		fmt.Printf("    %-9s : расход %d mB/cycle = %.3f mB/t, выход %.0f / %.0f EU/t (база/+упл)",
			name, fuel.Amount, float64(fuel.Amount)/float64(g.CycleTick), base, seal)
		if yieldFromCrude > 0 {
			fmt.Printf(", в НПЗ: %d/%d (%.0f%% от нефти)",
				yieldFromCrude, crudeIn, float64(yieldFromCrude)/float64(crudeIn)*100)
		}
		fmt.Println()
	}
	fmt.Println()
}

func analyzeFuel(derrickPerCycle int, cfg Config, derricks int, fuelName string, limitRefineries, limitGens float64, t table.Writer) {
	d := cfg.Machine.OilDerrick
	r := findRecipe(cfg.Machine.OilRefinery.Recipes, "craft_fractions")
	g := cfg.Machine.OilGenerator
	fuel := g.Fuels[fuelName]

	derrickRate := float64(derrickPerCycle) / float64(d.CycleTick)
	totalCrude := derrickRate * float64(derricks)

	// НПЗ — считаем сколько ИХ нужно чтобы покрыть всю выработку вышек
	crudeInput := float64(findIO(r.Input, "crude_oil"))
	refMaxPerOne := crudeInput / float64(r.CycleTick)
	refineriesNeeded := 0.0
	if refMaxPerOne > 0 {
		refineriesNeeded = totalCrude / refMaxPerOne
	}
	// Вся нефть перерабатывается (НПЗ масштабируется), вышки никогда не простаивают
	crudeConsumed := totalCrude

	// Только это топливо — сколько НПЗ его делает (mB/t)
	yieldRatio := float64(findIO(r.Output, fuelName)) / crudeInput
	fuelAvailable := crudeConsumed * yieldRatio

	perGen := float64(fuel.Amount) / float64(g.CycleTick)
	baseEU := float64(fuel.Energy) / float64(g.CycleTick)
	sealEU := baseEU * (1.0 + float64(g.UpgradeBoost)/100.0)
	if sealEU > float64(g.MaxOutputEU) {
		sealEU = float64(g.MaxOutputEU)
	}
	if baseEU > float64(g.MaxOutputEU) {
		baseEU = float64(g.MaxOutputEU)
	}

	activeGens := 0.0
	if perGen > 0 {
		activeGens = fuelAvailable / perGen
	}

	powerOutBase := activeGens * baseEU
	powerOutSeal := activeGens * sealEU

	// Затраты — вышки на 100%, НПЗ масштабируется по выработке (refineriesNeeded — дробное число)
	derrickEU := float64(d.EnergyCost) / float64(d.CycleTick) * float64(derricks)
	refineryEU := float64(r.EnergyCost) / float64(r.CycleTick) * refineriesNeeded
	totalIn := derrickEU + refineryEU

	netBase := powerOutBase - totalIn
	netSeal := powerOutSeal - totalIn

	effBase := 0.0
	effSeal := 0.0
	if powerOutBase > 0 {
		effBase = netBase / powerOutBase * 100
	}
	if powerOutSeal > 0 {
		effSeal = netSeal / powerOutSeal * 100
	}

	t.AppendRow(table.Row{
		derrickPerCycle,
		fmt.Sprintf("%.2f", derrickRate),
		fmt.Sprintf("%.2f", totalCrude),
		colorByLimit(refineriesNeeded, limitRefineries, fmt.Sprintf("%.2f", refineriesNeeded)),
		colorByLimit(activeGens, limitGens, fmt.Sprintf("%.2f", activeGens)),
		fmt.Sprintf("%.0f EU/t", powerOutBase),
		fmt.Sprintf("%.0f EU/t", powerOutSeal),
		fmt.Sprintf("%.0f EU/t", totalIn),
		fmt.Sprintf("%+.0f EU/t", netBase),
		fmt.Sprintf("%+.0f EU/t", netSeal),
		fmt.Sprintf("%.1f%%", effBase),
		fmt.Sprintf("%.1f%%", effSeal),
	})
}

// ── Утилиты ──────────────────────────────────────────────────────

func sortedFuelNames(fuels map[string]Fuel) []string {
	names := make([]string, 0, len(fuels))
	for n := range fuels {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func loadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func loadAnalyze(path string) (AnalyzeConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AnalyzeConfig{}, err
	}
	var ac AnalyzeConfig
	if err := json.Unmarshal(data, &ac); err != nil {
		return AnalyzeConfig{}, err
	}
	return ac, nil
}

func evenlyDistributed(min, max, count int) []int {
	if count <= 1 {
		return []int{min}
	}
	out := make([]int, count)
	step := float64(max-min) / float64(count-1)
	for i := 0; i < count; i++ {
		out[i] = min + int(step*float64(i)+0.5)
	}
	return out
}

func findRecipe(rs []RefineryRecipe, id string) RefineryRecipe {
	for _, r := range rs {
		if r.ID == id {
			return r
		}
	}
	return RefineryRecipe{}
}

func findIO(list []FluidIO, fluid string) int {
	for _, f := range list {
		if f.Fluid == fluid {
			return f.Amount
		}
	}
	return 0
}

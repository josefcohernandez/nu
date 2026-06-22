//go:build !race

package runtime

// spikeRaceEnabled es false fuera de `-race`: los tiempos del veto son los reales
// (coste de cómputo), así que TestSpikeMeasureVeto emite un veredicto firme. Ver
// spike_race_on_test.go para el caso bajo -race.
const spikeRaceEnabled = false

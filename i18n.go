package main

import (
	"fmt"
	"strings"
	"time"
)

// messages holds every string the server draws, in each supported language.
// Weather text (conditions, forecasts, warnings) comes from ECCC already in
// the chosen language and isn't translated here.
var messages = map[string]map[string]string{
	// Tile states.
	"tile_on":      {LangEN: "ON", LangFR: "ALLUMÉ"},
	"tile_off":     {LangEN: "OFF", LangFR: "ÉTEINT"},
	"tile_na":      {LangEN: "N/A", LangFR: "N/D"},
	"tile_run":     {LangEN: "RUN", LangFR: "LANCER"},
	"tile_open":    {LangEN: "OPEN", LangFR: "OUVERT"},
	"tile_closed":  {LangEN: "CLOSED", LangFR: "FERMÉ"},
	"tile_playing": {LangEN: "PLAYING", LangFR: "LECTURE"},
	"tile_paused":  {LangEN: "PAUSED", LangFR: "PAUSE"},

	// Header and weather pane.
	"weather_old":     {LangEN: "(old)", LangFR: "(ancien)"},
	"weather_updated": {LangEN: "Updated %s", LangFR: "Mis à jour à %s"},
	"weather_none":    {LangEN: "No weather data", LangFR: "Aucune donnée météo"},
	"observed_at":     {LangEN: "Observed at %s", LangFR: "Observé à %s"},
	"feels_like":      {LangEN: "Feels like %s°", LangFR: "Ressenti %s°"},
	"humidity":        {LangEN: "Humidity %s%%", LangFR: "Humidité %s %%"},
	"pressure":        {LangEN: "Pressure %s kPa", LangFR: "Pression %s kPa"},
	"visibility":      {LangEN: "Visibility %s km", LangFR: "Visibilité %s km"},
	"wind":            {LangEN: "Wind %s", LangFR: "Vent %s"},
	"wind_calm":       {LangEN: "calm", LangFR: "calme"},
	"wind_gust":       {LangEN: "gust %s", LangFR: "rafales %s"},
	"pop":             {LangEN: "%s%%", LangFR: "%s %%"},
	"temp_high":       {LangEN: "High %s°", LangFR: "Max %s°"},
	"temp_low":        {LangEN: "Low %s°", LangFR: "Min %s°"},
	"no_switches":     {LangEN: "No switches configured", LangFR: "Aucun interrupteur configuré"},

	// Errors and setup.
	"ha_unreachable":    {LangEN: "Home Assistant unreachable", LangFR: "Home Assistant injoignable"},
	"ha_token_rejected": {LangEN: "Home Assistant token rejected", LangFR: "Jeton Home Assistant refusé"},
	"setup_title":       {LangEN: "Set up this dashboard", LangFR: "Configurez ce tableau de bord"},
	"setup_hint":        {LangEN: "Open %s/settings on a phone or computer", LangFR: "Ouvrez %s/settings sur un téléphone ou un ordinateur"},
}

// T returns the message for key in lang, formatted with args. It falls back
// to English, then to the key itself, so a missing string is visible but harmless.
func T(lang, key string, args ...any) string {
	m := messages[key]
	s, ok := m[lang]
	if !ok {
		s, ok = m[LangEN]
	}
	if !ok {
		s = key
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// Canadian French writes times as "15 h 04"; English uses "15:04".
func formatClock(lang string, t time.Time) string {
	if lang == LangFR {
		return t.Format("15 h 04")
	}
	return t.Format("15:04")
}

var (
	frDays   = []string{"dim.", "lun.", "mar.", "mer.", "jeu.", "ven.", "sam."}
	frMonths = []string{"janv.", "févr.", "mars", "avr.", "mai", "juin", "juill.", "août", "sept.", "oct.", "nov.", "déc."}
)

// formatDate gives a short date: "Mon Nov 23" or "lun. 23 nov.".
func formatDate(lang string, t time.Time) string {
	if lang == LangFR {
		return fmt.Sprintf("%s %d %s", frDays[t.Weekday()], t.Day(), frMonths[t.Month()-1])
	}
	return t.Format("Mon Jan 2")
}

// formatWeekday gives a short weekday: "Tue" or "mar.".
func formatWeekday(lang string, t time.Time) string {
	if lang == LangFR {
		return frDays[t.Weekday()]
	}
	return t.Format("Mon")
}

// localTime rewrites an "HH:MM ZONE" string (Weather.Observed) for lang.
func localTime(lang, s string) string {
	if lang != LangFR {
		return s
	}
	hm, zone, _ := strings.Cut(s, " ")
	h, m, ok := strings.Cut(hm, ":")
	if !ok || len(h) != 2 || len(m) != 2 {
		return s
	}
	return strings.TrimSpace(h + " h " + m + " " + zone)
}

// WindText gives e.g. "WNW 22 km/h, gust 38" or "ONO 22 km/h, rafales 38".
func WindText(lang string, w Wind) string {
	if w.Speed == "" {
		return ""
	}
	if w.Calm() {
		return T(lang, "wind_calm")
	}
	s := strings.TrimSpace(w.Direction + " " + w.Speed + " km/h")
	if w.Gust != "" {
		s += ", " + T(lang, "wind_gust", w.Gust)
	}
	return s
}

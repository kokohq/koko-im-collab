package main

import (
	"context"
	"strings"
	"sync"
	"time"
)

// NLP foundation for koko command parsing.
// Designed for English first with locale-aware hooks for future multilingual support.

type NLPCommandInterpretation struct {
	Command      string
	Domain       string
	Argument     string
	LanguageCode string
	Confidence   float64
	ExpandedText string
}

var (
	nlpLearnedMu         sync.RWMutex
	nlpLearnedAcronyms   = map[string]map[string]string{}
	nlpLearnedPhrases    = map[string]map[string]string{}
	nlpContextCacheMu    sync.RWMutex
	nlpContextLearnCache = map[string]nlpContextLearningCache{}
)

type nlpContextLearningCache struct {
	Acronyms  map[string]string
	Phrases   map[string]string
	ExpiresAt time.Time
}

var nlpDialectRewrites = map[string]string{
	"gimme":        "give me",
	"wanna":        "want to",
	"gotta":        "got to",
	"pls":          "please",
	"plz":          "please",
	"thx":          "thanks",
	"ta":           "thanks",
	"cheers":       "thanks",
	"summarise":    "summarize",
	"analyse":      "analyze",
	"favour":       "favor",
	"organise":     "organize",
	"organisation": "organization",
	"behaviour":    "behavior",
	"optimise":     "optimize",
	"prioritise":   "prioritize",
	"whilst":       "while",
	"reckon":       "think",
	"arvo":         "afternoon",
	"uni":          "university",
}

var nlpCommonAcronyms = map[string]string{
	"asap": "as soon as possible",
	"eod":  "end of day",
	"eta":  "estimated time of arrival",
	"fyi":  "for your information",
	"imo":  "in my opinion",
	"imho": "in my humble opinion",
	"tbd":  "to be decided",
	"wip":  "work in progress",
	"ytd":  "year to date",
	"qoq":  "quarter over quarter",
	"mom":  "month over month",
	"yoy":  "year over year",
	"qtd":  "quarter to date",
	"mtd":  "month to date",
	"rfi":  "request for information",
	"rfp":  "request for proposal",
}

var nlpDomainAcronyms = map[string]map[string]string{
	featureModeRevenue: {
		"arr":       "annual recurring revenue",
		"mrr":       "monthly recurring revenue",
		"acv":       "annual contract value",
		"p&l":       "profit and loss",
		"pnl":       "profit and loss",
		"gst":       "goods and services tax",
		"vat":       "value added tax",
		"hmrc":      "his majesty revenue and customs",
		"ato":       "australian taxation office",
		"irs":       "internal revenue service",
		"bas":       "business activity statement",
		"payg":      "pay as you go",
		"sales-tax": "sales tax",
	},
	featureModeFinance: {
		"ap":      "accounts payable",
		"ar":      "accounts receivable",
		"gl":      "general ledger",
		"p&l":     "profit and loss",
		"pnl":     "profit and loss",
		"ebitda":  "earnings before interest taxes depreciation and amortization",
		"ifrs":    "international financial reporting standards",
		"gaap":    "generally accepted accounting principles",
		"uk-gaap": "united kingdom generally accepted accounting principles",
		"aasb":    "australian accounting standards board",
		"fasb":    "financial accounting standards board",
		"super":   "superannuation",
		"vat":     "value added tax",
		"hmrc":    "his majesty revenue and customs",
		"ato":     "australian taxation office",
		"irs":     "internal revenue service",
		"bas":     "business activity statement",
		"payg":    "pay as you go",
	},
	featureModeIndustrial: {
		"sop":  "standard operating procedure",
		"rca":  "root cause analysis",
		"mttr": "mean time to recovery",
		"oee":  "overall equipment effectiveness",
		"whs":  "work health and safety",
		"hse":  "health safety and environment",
		"ppe":  "personal protective equipment",
	},
}

var nlpLocaleSignals = map[string][]string{
	"en-GB": {"hmrc", "vat", "value added tax", "his majesty revenue and customs", "u.k.", "uk", "organisation", "summarise", "analyse"},
	"en-AU": {"ato", "bas", "payg", "australian taxation office", "business activity statement", "pay as you go", "superannuation", "whs", "arvo", "mate"},
	"en-US": {"irs", "sales tax", "quarterly filing", "color", "organize"},
}

func nlpNormalizeEnglish(input string) string {
	text := strings.TrimSpace(strings.ToLower(input))
	if text == "" {
		return ""
	}
	parts := strings.Fields(text)
	for i, p := range parts {
		if r, ok := nlpDialectRewrites[p]; ok {
			parts[i] = r
		}
	}
	return strings.Join(parts, " ")
}

func nlpExpandAcronyms(input, domain string) string {
	text := input
	tokens := strings.Fields(input)
	for i, t := range tokens {
		raw := strings.Trim(strings.ToLower(t), ",.!?;:()[]{}\"'")
		if raw == "" {
			continue
		}

		if m := nlpDomainAcronyms[strings.ToLower(strings.TrimSpace(domain))]; m != nil {
			if expanded, ok := m[raw]; ok {
				tokens[i] = expanded
				continue
			}
		}

		nlpLearnedMu.RLock()
		learnedDomain := nlpLearnedAcronyms[strings.ToLower(strings.TrimSpace(domain))]
		nlpLearnedMu.RUnlock()
		if learnedDomain != nil {
			if expanded, ok := learnedDomain[raw]; ok {
				tokens[i] = expanded
				continue
			}
		}

		if expanded, ok := nlpCommonAcronyms[raw]; ok {
			tokens[i] = expanded
			continue
		}
	}
	text = strings.Join(tokens, " ")
	return text
}

func nlpExpandAcronymsWithContext(input string, domain string, learned map[string]string) string {
	tokens := strings.Fields(input)
	for i, t := range tokens {
		raw := strings.Trim(strings.ToLower(t), ",.!?;:()[]{}\"'")
		if raw == "" {
			continue
		}
		if learned != nil {
			if expanded, ok := learned[raw]; ok {
				tokens[i] = expanded
				continue
			}
		}
		if m := nlpDomainAcronyms[strings.ToLower(strings.TrimSpace(domain))]; m != nil {
			if expanded, ok := m[raw]; ok {
				tokens[i] = expanded
				continue
			}
		}
		if expanded, ok := nlpCommonAcronyms[raw]; ok {
			tokens[i] = expanded
			continue
		}
	}
	return strings.Join(tokens, " ")
}

func nlpApplyPhraseRewrites(input string, rewrites map[string]string) string {
	if len(rewrites) == 0 {
		return input
	}
	result := input
	for src, dst := range rewrites {
		s := strings.ToLower(strings.TrimSpace(src))
		d := strings.TrimSpace(dst)
		if s == "" || d == "" {
			continue
		}
		result = strings.ReplaceAll(result, s, d)
	}
	return result
}

func nlpDetectEnglishLocale(input string) string {
	lower := strings.ToLower(input)
	for locale, signals := range nlpLocaleSignals {
		for _, sig := range signals {
			if strings.Contains(lower, sig) {
				return locale
			}
		}
	}
	return "en"
}

func nlpPreprocessEnglish(input, domain string) (string, string) {
	norm := nlpNormalizeEnglish(input)
	expanded := nlpExpandAcronyms(norm, domain)
	return expanded, nlpDetectEnglishLocale(expanded)
}

func nlpLearningCacheKey(accountID, groupName, domain, locale string) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(accountID)),
		strings.ToLower(strings.TrimSpace(groupName)),
		strings.ToLower(strings.TrimSpace(domain)),
		strings.ToLower(strings.TrimSpace(locale)),
	}, "::")
}

func nlpGetContextLearning(store *AppStore, accountID, groupName, domain, locale string) (map[string]string, map[string]string) {
	if store == nil || strings.TrimSpace(accountID) == "" {
		return nil, nil
	}
	domain = nlpNormScopeValue(domain, featureModeAll)
	locale = nlpNormScopeValue(locale, "en")
	key := nlpLearningCacheKey(accountID, groupName, domain, locale)

	nlpContextCacheMu.RLock()
	entry, ok := nlpContextLearnCache[key]
	nlpContextCacheMu.RUnlock()
	if ok && time.Now().Before(entry.ExpiresAt) {
		return entry.Acronyms, entry.Phrases
	}

	acronyms := map[string]string{}
	phrases := map[string]string{}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if rows, err := store.ListNLPAcronymExpansions(ctx, accountID, groupName, domain, locale); err == nil {
		for _, r := range rows {
			acronyms[strings.ToLower(strings.TrimSpace(r.ShortForm))] = strings.TrimSpace(r.ExpandedForm)
		}
	}
	if rows, err := store.ListNLPPhraseRewrites(ctx, accountID, groupName, domain, locale); err == nil {
		for _, r := range rows {
			phrases[strings.ToLower(strings.TrimSpace(r.SourcePhrase))] = strings.TrimSpace(r.NormalizedPhrase)
		}
	}

	nlpContextCacheMu.Lock()
	nlpContextLearnCache[key] = nlpContextLearningCache{
		Acronyms:  acronyms,
		Phrases:   phrases,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}
	nlpContextCacheMu.Unlock()

	return acronyms, phrases
}

func nlpInvalidateContextLearningCache(accountID, groupName string) {
	accountID = strings.ToLower(strings.TrimSpace(accountID))
	groupName = strings.ToLower(strings.TrimSpace(groupName))
	nlpContextCacheMu.Lock()
	defer nlpContextCacheMu.Unlock()
	for key := range nlpContextLearnCache {
		parts := strings.Split(key, "::")
		if len(parts) != 4 {
			continue
		}
		if parts[0] != accountID {
			continue
		}
		if groupName != "" && parts[1] != groupName {
			continue
		}
		delete(nlpContextLearnCache, key)
	}
}

func nlpPreprocessEnglishWithContext(store *AppStore, accountID, groupName, input, domain string) (string, string) {
	norm := nlpNormalizeEnglish(input)
	locale := nlpDetectEnglishLocale(norm)
	learnedAcronyms, learnedPhrases := nlpGetContextLearning(store, accountID, groupName, domain, locale)
	withPhrases := nlpApplyPhraseRewrites(norm, learnedPhrases)
	expanded := nlpExpandAcronymsWithContext(withPhrases, domain, learnedAcronyms)
	return expanded, nlpDetectEnglishLocale(expanded)
}

func nlpInterpretKokoCommand(text, currentDomain string) NLPCommandInterpretation {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return NLPCommandInterpretation{LanguageCode: "en", Confidence: 0}
	}

	stripped, ok := trimKokoPrefix(trimmed)
	if !ok {
		return NLPCommandInterpretation{LanguageCode: "en", Confidence: 0}
	}

	norm := nlpNormalizeEnglish(stripped)
	if norm == "" {
		return NLPCommandInterpretation{LanguageCode: "en", Confidence: 0}
	}

	effectiveDomain := normalizedDomainKey(currentDomain)
	expanded, locale := nlpPreprocessEnglish(norm, effectiveDomain)

	contains := func(keys ...string) bool {
		for _, k := range keys {
			if strings.Contains(expanded, k) {
				return true
			}
		}
		return false
	}

	inferDomain := func() string {
		switch {
		case contains("finance", "cash flow", "accounts payable", "accounts receivable", "general ledger", "ifrs", "gaap", "superannuation"):
			return featureModeFinance
		case contains("revenue", "profit and loss", "goods and services tax", "sales", "pipeline"):
			return featureModeRevenue
		case contains("industrial", "factory", "maintenance", "overall equipment effectiveness"):
			return featureModeIndustrial
		case contains("electronics", "pcb", "firmware", "chip"):
			return featureModeElectronics
		case contains("consumer", "lifestyle"):
			return featureModeConsumer
		default:
			if effectiveDomain != featureModeAll {
				return effectiveDomain
			}
			return ""
		}
	}

	domain := inferDomain()
	if contains("summarize", "summary", "brief") {
		return NLPCommandInterpretation{Command: telegramSummarizeCommand, Domain: domain, LanguageCode: locale, Confidence: 0.72, ExpandedText: expanded}
	}
	if contains("analyze", "analysis", "diagnose") {
		return NLPCommandInterpretation{Command: telegramAnalyzeCommand, Domain: domain, LanguageCode: locale, Confidence: 0.72, ExpandedText: expanded}
	}
	if contains("act", "execute", "run the actions", "take action") {
		return NLPCommandInterpretation{Command: telegramActCommand, Domain: domain, LanguageCode: locale, Confidence: 0.72, ExpandedText: expanded}
	}
	if contains("help", "commands", "what can you do") {
		return NLPCommandInterpretation{Command: telegramHelpCommand, Domain: domain, LanguageCode: locale, Confidence: 0.7, ExpandedText: expanded}
	}
	if contains("plan", "pricing", "subscription") {
		return NLPCommandInterpretation{Command: telegramPlanCommand, Domain: domain, LanguageCode: locale, Confidence: 0.7, ExpandedText: expanded}
	}
	if contains("switch group", "set group", "change group") {
		return NLPCommandInterpretation{Command: telegramGroupCommand, Domain: domain, Argument: extractTrailingPhrase(expanded, "group"), LanguageCode: locale, Confidence: 0.68, ExpandedText: expanded}
	}

	return NLPCommandInterpretation{LanguageCode: locale, Confidence: 0.2, ExpandedText: expanded}
}

func nlpLearnAcronym(domain, shortForm, expanded string) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	shortForm = strings.ToLower(strings.TrimSpace(shortForm))
	expanded = strings.TrimSpace(expanded)
	if shortForm == "" || expanded == "" {
		return
	}
	nlpLearnedMu.Lock()
	defer nlpLearnedMu.Unlock()
	if nlpLearnedAcronyms[domain] == nil {
		nlpLearnedAcronyms[domain] = map[string]string{}
	}
	nlpLearnedAcronyms[domain][shortForm] = expanded
}

func nlpLearnPhrase(domain, sourcePhrase, normalizedPhrase string) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	sourcePhrase = strings.ToLower(strings.TrimSpace(sourcePhrase))
	normalizedPhrase = strings.TrimSpace(normalizedPhrase)
	if sourcePhrase == "" || normalizedPhrase == "" {
		return
	}
	nlpLearnedMu.Lock()
	defer nlpLearnedMu.Unlock()
	if nlpLearnedPhrases[domain] == nil {
		nlpLearnedPhrases[domain] = map[string]string{}
	}
	nlpLearnedPhrases[domain][sourcePhrase] = normalizedPhrase
}

func extractTrailingPhrase(text, marker string) string {
	idx := strings.Index(text, marker)
	if idx < 0 {
		return ""
	}
	v := strings.TrimSpace(text[idx+len(marker):])
	if strings.HasPrefix(v, "to ") {
		v = strings.TrimSpace(strings.TrimPrefix(v, "to "))
	}
	return v
}

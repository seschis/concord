package triage

import "strings"

// A judge persona is a focus/personality layered on top of the shared
// adjudication task. Only the focus varies per judge; the JSON output contract
// (adjudicationContract) is fixed and always appended, so every judge on a panel
// emits the same comparable schema and the panel can vote on verdicts.
//
// Personas are focus text only (the persona line plus a decision rubric). They
// are intentionally distinct so a panel of judges on the SAME model still
// disagrees in useful ways.

const defaultJudgeFocus = `You are a senior application security engineer adjudicating between independent triage analyses of the same finding. The analysts reached different verdict categories and you must determine the final verdict.

Consider which analysis traced the data flow more completely, which correctly identified or missed mitigations, which is more grounded in what the code actually shows, and whether facts from one analysis invalidate another's conclusion.`

// adjudicationContract is the fixed JSON output contract shared by every judge.
const adjudicationContract = `Respond with ONLY a valid JSON object, no markdown, no text outside the JSON:
{
  "agreement_analysis": "<where the analyses agree and disagree>",
  "stronger_analysis": "<which model's analysis is stronger, or 'neither'>",
  "final_verdict": "CONFIRMED_REAL|LIKELY_REAL|UNLIKELY|NOT_EXPLOITABLE|NEEDS_MORE_CONTEXT",
  "final_confidence": "HIGH|MEDIUM|LOW",
  "final_reasoning": "<comprehensive reasoning drawing on the analyses>",
  "key_deciding_factor": "<the single most important factor>"
}`

var builtInPersonas = map[string]string{
	"adjudicator": defaultJudgeFocus,
	"strict":      `You are a skeptical, evidence-first security reviewer. Assume the finding is a false positive until the code proves otherwise. Judge the analyses by the strength of their concrete evidence: a complete, grounded data-flow trace from an attacker-controlled source to the sink, and the mitigations or sanitizers the code actually applies. Treat hand-waving, unverified reachability, and "likely" claims as weak. If the evidence does not clearly establish exploitability, prefer NOT_EXPLOITABLE or UNLIKELY. Do not reward speculation.`,
	"business":    `You are a pragmatic application security engineer focused on real-world risk. Judge the analyses by how plausible an actual attack is: can an attacker realistically reach this code path, what is the blast radius and impact on data or users if exploited, and how severe is that in the context of the application? Weight the verdict toward the interpretation that best reflects true exploitable risk and business impact, not a theoretical worst case. Distinguish a genuinely reachable, high-impact issue from a theoretical or low-impact one.`,
	"codeflow":    `You are a code-flow and taint-analysis specialist. Judge the analyses by how completely and accurately they trace the data path from the untrusted source to the sink. Is the source genuinely attacker-controlled? Does the data reach the sink without effective neutralization? Did any analysis miss a sanitizer, validation, or encoding that breaks the flow? Favor the analysis whose trace is most complete and best grounded in the actual code, and let gaps in the trace drive the verdict.`,
}

// FocusFor returns the focus/personality text for a named built-in judge
// persona. It reports false for an unknown name.
func FocusFor(name string) (string, bool) {
	f, ok := builtInPersonas[name]
	return f, ok
}

// BuiltInPersonaNames lists the built-in judge personas in a stable order.
func BuiltInPersonaNames() []string {
	return []string{"adjudicator", "strict", "business", "codeflow"}
}

// AnalystPersonaNames lists the personas usable as triage-analyzer lenses — the
// diverse "lens" personas. The neutral "adjudicator" persona is the default judge
// and is deliberately NOT offered as an analyst lens: its focus is framed for
// adjudicating between several analyses, which is incoherent for a single
// triage analyst. Keep in sync with the lens entries above.
func AnalystPersonaNames() []string {
	return []string{"strict", "business", "codeflow"}
}

// BuildJudgeSystemPrompt composes a judge's full system prompt from a focus
// (persona) block plus the fixed JSON output contract. The contract is always
// appended so all judges on a panel stay comparable.
func BuildJudgeSystemPrompt(focus string) string {
	return strings.TrimSpace(focus) + "\n\n" + adjudicationContract
}

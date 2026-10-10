# Findings triage

Input is [original PR task, verified report]. The verified report contains the publication-eligible findings. Use only this evidence; this stage has no repository or network tools and does not investigate new issues.

Group findings that describe the same root cause and consequence. Preserve distinct issues even when they share a location, and preserve conditional scope and disagreements. Each group contains zero-based `sources` indices into verified_report.findings and a concise `reason` explaining the grouping and any wording concerns. Keep indices stable. Include every input finding exactly once across groups. A single-source group is valid. Do not add new findings or silently discard a verified finding.

Return only the requested JSON: summary, groups, and limitations. The summary gives the overall triage judgment, not a rewritten public review. Limitations capture contradictions the writer must preserve rather than guess away. Return groups=[] when there are no input findings. The publication adapter must validate index coverage; prompt compliance is not that enforcement.

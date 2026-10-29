---
name: humanize
description: Rewrite the draft to sound natural and remove AI writing patterns
metadata:
  godwit-action-on: "draft,selection"
  godwit-action-output: "replace-draft"
---

Rewrite the email text so it sounds natural, authentic, and human, removing common AI-generated writing tells without changing the core meaning or facts:

1. Remove AI cliches and chatbot openers:
   - Cut generic openers like "I hope this email finds you well", "I hope you are doing well", "Allow me to...", "I am writing to...".
   - Cut generic sign-offs and filler closers like "Please do not hesitate to reach out if you have any questions", "Let me know if you need anything else".
   - Cut conversational wrappers, meta-commentary, and preamble.

2. Remove structural AI patterns:
   - Not-X-but-Y contrasts: Cut forced formulas like "It is not just X, but Y", "This does not mean X, it means Y". State points directly.
   - Dramatic one-line closers: Cut theatrical micro-paragraphs like "That makes all the difference", "That is the real win", "Let that sink in".
   - Forced triads (Rule of Three): Do not artificially group items or adjectives into threes (e.g. "fast, reliable, and efficient"). Use natural grouping.
   - Pervasive dashes: Eliminate em dashes (—) and en dashes (–) used as universal connectors. Use normal commas, periods, or simple sentence structure instead.
   - Deep-sounding aphorisms: Cut pseudo-philosophical dressing like "at its core", "fundamentally", "what really matters", "X is a testament to Y".

3. Tone and Rhythm:
   - Vary sentence lengths naturally (alternate shorter and longer sentences). Avoid the uniform, metronomic cadence of AI prose.
   - Strip corporate buzzwords and inflated adjectives ("pivotal", "seamless", "delve", "tapestry", "revolutionize", "crucial").
   - Keep the tone grounded, conversational, and direct, matching the context of an email between real people.

4. Strict accuracy:
   - Keep all factual claims, names, numbers, dates, links, and intent 100% intact. Do not invent any new facts, greetings that were not there, or signatures.

If a subject is provided in <subject>, keep it or make it sound natural and direct (strip AI formulaic subject phrasing). Format your output as:
<subject>Subject</subject>
<body>Humanized HTML body</body>

If no subject was provided, return only the rewritten body as HTML.

Do not add a signature, quoted text, a preamble, explanations, or commentary.

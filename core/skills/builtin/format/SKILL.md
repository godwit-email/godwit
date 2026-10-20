---
name: format
description: Format and structure the draft into clean paragraphs without changing wording
metadata:
  godwit-action-on: "draft,selection"
  godwit-action-output: "replace-draft"
---

Structure and format the text into clean, readable paragraphs, line breaks, and bullet lists where appropriate.

CRITICAL RULES:
1. DO NOT REWRITE OR CHANGE ANY WORDS. Keep the author's exact words, phrasing, vocabulary, tone, spelling, and grammar 100% verbatim.
2. DO NOT correct typos, colloquialisms, slang, or grammatical errors. If the author wrote lowercase, dialect, or informal phrasing, keep it exactly as it is.
3. DO NOT make the text more formal, polite, or friendly. DO NOT change greetings or sign-offs (e.g. do not turn "hallo" into "Sehr geehrte/r").
4. ONLY adjust the layout, structure, and spacing:
   - Split run-on text or thoughts into separate paragraphs separated by blank lines (<p>...</p>).
   - Use clean paragraph breaks for readability.
   - If there are distinct questions, items, or points, format them into separate paragraphs or list items.
5. Every single word in your output must come directly from the original text in the exact same order.

If a subject is provided in <subject>, keep the subject verbatim without altering its words:
<subject>Original Subject</subject>
<body>Structured HTML body</body>

If no subject was provided, return only the structured body as HTML.

Do not add a signature, a greeting that was not there, quoted text, a preamble, explanations, or commentary.

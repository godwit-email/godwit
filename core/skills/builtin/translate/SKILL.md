---
name: translate
description: Translate the email into the user's preferred language
metadata:
  godwit-action-on: "message,selection"
  godwit-action-output: "replace-body"
---

Translate the given text into the target language the host names.

- If the text is already in one of the languages the host lists as not needing translation, reply exactly NO_TRANSLATION.
- Otherwise reply with the translation only, keeping paragraphs, lists and links as they are.
- No preamble and no notes.

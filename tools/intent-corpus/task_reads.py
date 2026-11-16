"""Curated read grammar compositions and contrasting data/authority boundaries."""


def task_read_cases():
    objects = ["tasks", "todos", "to-do list", "to‑dos", "reminders", "action items", "checklist", "personal tasks"]
    states = [("", "open"), ("open", "open"), ("pending", "open"), ("completed", "completed"), ("finished", "completed"), ("cancelled", "cancelled"), ("all", "all")]
    windows = ["", "today", "tomorrow", "yesterday", "this week", "next week", "last week"]
    forms = [
        ("bare", "{body}?"),
        ("show", "show my {body}?"),
        ("list", "list {body}."),
        ("availability", "any {body}?"),
        ("possessive_question", "what {body} do I have?"),
        ("which", "which {body} are on my list?"),
        ("count", "how many {body} do I have?"),
        ("polite_display", "can you display my {body}, please?"),
        ("polite_pull_up", "could you please pull up my {body}?"),
        ("bring_up", "please bring up my {body}!"),
        ("polite_see_unicode", "may I see my {body}？"),
    ]
    for obj in objects:
        for modifier, state in states:
            for window in windows:
                body = " ".join(p for p in [modifier, obj, window] if p)
                for family, form in forms:
                    text = form.format(body=body)
                    yield dict(family="task_read_" + family, input=text, match=True, generic=True,
                               state=state, window=window, reminders=obj == "reminders", count=family == "count")
                # Keep data, negation and conditional wording distinct from an
                # actual read. These are not inferred by changing parser output.
                for family, form in [
                    ("quoted", '"show my {body}?"'),
                    ("code", "`show my {body}`"),
                    ("negated", "Do not show my {body}"),
                    ("reported", "They said: show my {body}"),
                    ("note", "Note: show my {body}"),
                    ("conditional", "If Mira asks, show my {body}"),
                ]:
                    yield dict(family="task_read_boundary_" + family, input=form.format(body=body), match=False)
    for head in ["What's on my plate", "What is on my plate", "What do I need to do", "What have I got to do", "What's left for me to do", "What have I left to do"]:
        for window in windows:
            yield dict(family="task_read_implicit_personal", input=" ".join(p for p in [head, window] if p) + "?",
                       match=True, generic=True, state="open", window=window, reminders=False, count=False)
    for topic in ["laundry", "Aurora", "the release", "the school trip", "my bicycle", "Mira", "Keychain", "the signing certificate", "the backup", "the dentist", "the invoice", "the workshop"]:
        for form in ["show my tasks about {topic}?", "which tasks mention {topic}?", "list reminders for {topic}"]:
            yield dict(family="task_read_topical", input=form.format(topic=topic), match=True, generic=False)
    for text in ["tasks", "todo", "todos", "tasks?!", "to‑dos？", "to–dos!", "to-do list…", "to do?", "task-list?", "tasklist?", "my tasks, please?", "please, show my tasks?", "show my tasks not done yet?", "show my overdue tasks?", "show my upcoming reminders?", "what are my action items?", "what are my remaining tasks?"]:
        # These deliberately test spelling/punctuation, separately from the
        # thousands of meaningful status/calendar/read-head combinations.
        yield dict(family="task_read_punctuation_and_status", input=text, match=True, generic=True)
    for text in ["What is a task?", "What are tasks?", "What does todo mean?", "What are reminders?", "Which tasks could improve Go code quality?", "What did I read about tasks yesterday?", "What did they say about reminders?", "task: show my tasks", "complete tasks", "cancel tasks", "add a task called todo", "421-12", "tasks = 2 + 3", "The tasks are completed", "Mira owns the action items", "Are background tasks expensive in Go?", "Show me examples of tasks", "Show the article about reminders", "check the task validator tomorrow", "read the reminder API proposal on Monday"]:
        yield dict(family="task_read_boundary_general_or_write", input=text, match=False)

# Sandbox tools

Use `/workspace` for task files and outputs. AIO provides shell execution, code execution, file operations, and desktop screenshots and input. Playwright controls the same visible Chromium instance on the desktop.

Use Playwright snapshots and element actions for web content. Use AIO desktop screenshots and input for other applications, browser chrome, or native dialogs. Browser actions and desktop actions share focus, tabs, and state. Check the current page or screenshot when switching tools. Keep at least one browser window open; closing the last window ends the supervised browser session.

These tools act as the same sandbox user and can change sandbox files and processes. Follow the task's scope. Report only actions and results you actually observed.

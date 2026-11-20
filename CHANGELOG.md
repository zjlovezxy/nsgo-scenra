# Changelog

## [Unreleased]

## [0.2.1] - 2026-11-20

- Fix a window that drew nothing when the agents' machine changed: the window's content is now a laid-out view rather than the region itself.
- Keep **Settings** → **Agent machine**'s switch for the next start, and say on screen what turning it on does.

## [0.2.0] - 2026-11-20

- Add running a folder's agents on another machine over `ssh`: `scenra serve` serves the folder's agents with an address and a token, which **Settings** → **Agent machine** takes; the window only draws, and the assistant there reads the files instead of the view.
- Keep each folder's agent and model for its new conversations, instead of one choice shared by every folder.
- Build releases from this fork's GitHub Actions with the macOS arm64, Windows and Linux targets, without the update key upstream signs with, so these builds do not update themselves.

## [0.1.1] - 2026-11-18

- Add a language setting choosing English, Simplified Chinese or the system's language, which applies at once and is kept for later starts.
- Redesign the website after Agio's, with screenshots of a music app's designs and flows in both languages and appearances.
- Fix a comment card opening at the corner of the icon or text pressed instead of beside the pointer.

## [0.1.0] - 2026-11-18

Scenra's first release: a desktop designer whose assistant, a coding agent you already use, draws an app's screens and flow charts in plain-text files.

- Add folder workspaces of plain-text `.scenra` files holding phone, tablet and desktop designs drawn from Tailwind-shaped primitives, or flow charts.
- Add an assistant run through Agio by Claude Code, Codex or another coding agent, which works on the selection, resolves comments and keeps each folder's conversations.
- Add components shared by designs, with variants, states, overrides and slots, shown together from **Components**.
- Add outlets filling a window's content with another design, and play designs as the app would through their taps.
- Add comments and notes on design elements, pictures from the folder's `assets/`, and each file's Git changes in the sidebar.
- Add a tab per folder beside a Home tab of recent folders and the settings, reopened at the next start.
- Add macOS, Windows and Linux builds in English and Simplified Chinese, which update themselves.

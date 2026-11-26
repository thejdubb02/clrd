# Unreleased changes

Add one Markdown file per user-visible PR to `added/`, `changed/`,
`fixed/`, `removed/` or `security/`. Pick a unique name, such as
`fixed/899-changelog-conflicts.md`. Put one bullet in the file, with
optional continuation lines indented by two spaces.

```markdown
- Explain the change in words a user can understand.
```

If your PR already added a bullet directly to `CHANGELOG.md`, move that
bullet to its own file here. For tests, CI and refactors with no visible
change, use the `no-changelog` label.

The server shows these files under Unreleased. The release command moves
them to the dated changelog and removes the files it consumed. The
`000-existing.md` files bundle the entries from before this layout.

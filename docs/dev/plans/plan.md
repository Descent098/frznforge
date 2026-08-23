This project is meant to be a static-site generator that is a **read-only** replacement for something like github for a single user. The intention being as a place for you to showcase your projects, and make them accessible to others, without allowing the other, more social interactions of platforms like github or codeberg. It's meant to be able to ingest from other sources to build as well, which also makes it handy for people who self-host services like forgejo, but don't want to make that accessible to the world. Essentially it's for source-available projects, not open-source community-driven projects. 

There are NO accounts, each instance is for 1 person's account and usage. 

## Process

Concurrently scans git repos and produces a JSON file with the git contents to be parsed. Uses that and the info from it to generate a read only site that can be publicly shared without risking having to share a private forge instance, or worrying about a host pulling the plug. The tradeoff is needing to manually rebuild to see changes, and no social-media style interactions like github. 

## Features Not Needed from similar sites

There's no accounts, so no need for:

- [ ] issue tracking
- [ ] Pull Requests
- [ ] Stars
- [ ] Forks
- [ ] Watching a repo

## Features

- [ ] Pages
    - [ ] Per repo
        - [ ] Analytics (show on main page)
            - [ ] Language breakdown (like github)
            - [ ] contributors
        - [ ] Configurable Metadata
            - [ ] Name
            - [ ] Links
                - [ ] Homepage
                - [ ] Issue/Bug Tracker
                - [ ] Donations
                - [ ] Upstream source (e.g. github repo)
            - [ ] Short Description (300 characters)
            - [ ] Tags
            - [ ] Template repo
                - [ ] Just changes UI to indicate you should clone and modify the repo
            - [ ] License
                - [ ] Detect by file, or specify some other way
        - [ ] Clone button (if possible without a running server)
        - [ ] Readme previewing (md -> html)
        - [ ] In-browser syntax-highlighted code browsing
        - [ ] Download Zip's of source code
        - [ ] Commit history
        - [ ] Branches
        - [ ] View tags
        - [ ] Insight analytics
            - [ ] Number of contributors
            - [ ] Number of commits
            - [ ] Lines of code over time
        - [ ] Releases
            - [ ] import github/forgejo/gitea/gitlab releases
            - [ ] on normal repos with no forge use the tag and tag message (as markdown) to generate release
                    - [ ] git tag -a v1.0.0 -m "Your tag message here"
                    - [ ] Can also configure this mode on a per-repo basis if someone hosts their code on a forge, but doesn't use the built in release system
    - [ ] Profile overview
        - [ ] Similar to https://github.com/Descent098
            - [ ] Which renders a profile readme, like https://github.com/Descent098/descent098
        - [ ] Show up to 10 pinned repo's
        - [ ] Show contribution graph
        - [ ] Show top languages
        - [ ] Show most recent commits as an event log
        - [ ] Links
            - [ ] Personal site(s)
            - [ ] LinkedIn
            - [ ] Email
            - [ ] Location
            - [ ] Workplace
            - [ ] School
            - [ ] Other Forge's
                - [ ] Github
                - [ ] Gitlab
                - [ ] Codeberg
                - [ ] Forgejo
                - [ ] Gitea
        - [ ] Basically can be just a markdown file where content shows on homepage, and with the frontmatter you can, set the repo's to pin, and links for the user
    - [ ] Repo Listing
        - [ ] Listing of all repos with simple filters/sorting and search
            - [ ] Sorting
                - [ ] Oldest/newest
                - [ ] Name (alphabetical)
            - [ ] Filters
                - [ ] Which languages were used
                - [ ] If it's a template repo or normal repo
                - [ ] Tags
            - [ ] Paginated to 50 rows per-page
            - [ ] Per repo card
                - [ ] Show project name
                - [ ] short description
                - [ ] Top 3 languages
                - [ ] Last time updated
    - [ ] Notes (inspired by https://gist.github.com/ )
        - [ ] A little section that allows you to have a folder full of either:
            - [ ] files
                - [ ] Rendered as single notes with either syntax highlighted view of the file, or for markdown a rendered view with the option to view it as a source instead of preview
                - [ ] examples:
                    - [ ] https://gist.github.com/Descent098/539f12d9fb0beda47c5e6640448f291a
                    - [ ] https://gist.github.com/haifei5625joselester/66f655d48d982d4cfcb702835c680136
            - [ ] folders
                - [ ] Contain multiple files that show up like multiple files do on gists
                - [ ] Example: https://gist.github.com/Descent098/90c0f507f6185360bfad639b6711601c
- [ ] Importers
    - [ ] Abilty to import repos from other providers
        - [ ] Github
        - [ ] Forgejo
        - [ ] Gitlab
        - [ ] Gitea
        - [ ] Local git repo
    - [ ] These should be runable as an interactive command to initially get setup with, then once the repos are chosen it should pull from the sources on each `npm run build`
- [ ] Organizations
    - [ ] Some way to group some repos under organizations that also can have their own `Profile overview`

## Technical details

- [ ] This repo is an astro repo with svelte built in, everything should be as static as possible, with svelte where interactivity is needed
- [ ] Use plain CSS, no frameworks like tailwind or Sass

## Design

- [ ] color scheme of orangey fire for latest more dynamic content, and blue ice for older, more static content
    - [ ] Other colors are plain white for light theme, dark-theme color similar to `gihub.png`
- [ ] Sidebar-driven nav with search and ctrl+k command pallete
    - [ ] Similar to `bitbucket1.png`, `bitbucket2.png`, `gitlab.png`

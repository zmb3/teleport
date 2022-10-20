---
authors: Jeff Pihach (jeff.pihach@goteleport.com)
state: draft
---

# RFD 89 - Merge Webapps into Teleport

## What

- Merge the [webapps](https://github.com/gravitational/webapps) repo into the
  [teleport](https://github.com/gravitational/teleport) repo.
- Merge the [webapps.e](https://github.com/gravitational/webapps.e) repo into
  the [teleport.e](https://github.com/gravitational/teleport.e) repo.

## Why

The version of the web UI and the Teleport are tightly coupled but the
repositories were created separate to support both Gravity and Teleport. This
introduced complexities when integrating the two projects.

We have to separately clone and build the web UI and commit the webasset
changes into the teleport repo whenever we want to represent an update. We also
have to maintain multiple build systems and ops procedures for the web UI.

Now that Gravity is no longer a product we develop, moving the web UI
repositories into their respective Teleport repo will save us time and
confusion.

## Success Criteria

Both [Teleport](https://github.com/gravitational/teleport) and
[Teleport.e](https://github.com/gravitational/teleport.e) repos contain their
respective UI counterparts and the build systems correctly build all projects.

## Developer Experience

### Day to day

The Day to day development experience shouldn't change. The webapps project
will continue to be self-contained and only require access to the nodejs and
yarn binaries. The locations of the enterprise assets will have to be changed
but that should remain transparent to the day to day activities.

To work on the enterprise version of the application the developer will have to
check out the enterprise version of Teleport which will bring with it the
appropriate webapp assets.

Merging these repositories may cause some disruption when multiple developers
are working on the same feature across the back and frontend. Using a
[git worktree](https://git-scm.com/docs/git-worktree) to create a worktree of
the webapps folder is an elegant way of approaching this issue

### Backports

To avoid having different proceses and build systems depending on the version
of teleport, all supported release branches (v9, v10, v11) will have their
respective versions of the webapps repository.

### Build process

The Teleport build process will need to be expanded to include nodejs and yarn
in order to be able to build the webapp assets and include into the final
teleport binaries. We will no longer be committing a compiled version of the
webassets into the teleport repository, the UI will need to be compiled from
source on the initial build of teleport. A make target will be provided to
perform a dockerized build of the webassets for those who don't wish to install
node and yarn on their workstation.

## Process

Below outlines the process for each repository in the order that the steps need
to be taken. All repositories will be prepped for the final archival and merging
but the final switch over steps will not be taken until v11 has been released.

The git histories of each branch will be maintained while merging. [Merging repositories](https://stackoverflow.com/questions/13040958/merge-two-git-repositories-without-breaking-file-history)

### Webapps repository

[repository](https://github.com/gravitational/webapps)

#### Actions

- [ ] Triage issues.
  - [ ] Close those that are no longer relevant.
  - [ ] [Transfer remaining issues](https://docs.github.com/en/issues/tracking-your-work-with-issues/transferring-an-issue-to-another-repository) to the [Teleport repo](https://github.com/gravitational/teleport)
  - [ ] Add the [ui label](https://github.com/gravitational/teleport/labels/ui) to
        those transferred issues.
- [ ] Triage PRs.
  - [ ] Close those that are no longer relevant.
  - [ ] Merge remaining PRs.
  - [ ] Apply the necessary backports.
- [ ] Update [the default path to tsh in dev mode](https://github.com/gravitational/webapps/blob/27c615b3ff6f317a85fac4aa28b8e73fa4aa0d28/packages/teleterm/src/mainProcess/runtimeSettings.ts#L18-L23) for Connect.

### Webapps.e repository

[repository](https://github.com/gravitational/webapps.e)

#### Actions

- [ ] Triage issues.
  - [ ] Close those that are no longer relevant.
  - [ ] [Transfer remaining issues](https://docs.github.com/en/issues/tracking-your-work-with-issues/transferring-an-issue-to-another-repository) to the [Teleport.e repo](https://github.com/gravitational/teleport.e)
  - [ ] Add the [ui label](https://github.com/gravitational/teleport.e/labels/ui)
- [ ] Triage PRs.
  - [ ] Close those that are no longer relevant.
  - [ ] Merge remaining PR's.
  - [ ] Apply the necessary backports.

### Teleport repository

[repository](https://github.com/gravitational/teleport)

#### Actions

- [ ] Remove `/webassets` submodule
  - This submodule is no longer required as the web UI will be built on-demand.
  - The folder will remain as the output location of the on-demand build but
    will not be committed.
- [ ] Clone the [Webapps repository](https://github.com/gravitational/webapps) into
      the Teleport root. [Maintaining their respective git histories](https://stackoverflow.com/questions/13040958/merge-two-git-repositories-without-breaking-file-history)
  - [ ] This will need to be done for every respective version branch (v9, v10, v11)
- [ ] Update targets that use the `packages/webapps.e` submodule to point points to
      the correct version in the `teleport.e/web` folder.
- [ ] Only require teleport build processes to run on teleport paths and the webapp
      ones to run on the webapp paths
-
- Archive the [Webapps repository](https://github.com/gravitational/webapps).

#### CI jobs

- **CodeQL / Analyze (go) (pull_request)**
- **Teleport-IntegrationTest (ci-account)**
- **Teleport-UnitTest (ci-account)**
- **Teleport-DocTest (ci-account)**
- **Teleport-Lint (ci-account)**
- **Teleport-OsCompatibility (ci-account)**
  - Disable for changes exclusively made in the `/web` path.
- **Assign / Auto Request Review**
  - Create a list of full stack engineers and have it pick from this list for
    changes made to the `/web` path.
- **Check / Checking reviewers**
  - Continue to require 2 reviewers for changes in the `web` projects.
- **Label / Label Pull Request (pull_request_target)**
  - Add label for UI to changes made in the `/web` path.
- **CodeQL / Analyze (javascript) (pull_request)**
  - Ensure that it's running for changes in `/web` path.
- **Code scanning results / CodeQL**
  - No changes
- **webapps-build**
  - Instead of having this job build webassets and push it, it should ensure
    that it can be built.
- **webapps-test**
  - Migrate from the webapps repository to the teleport repository
  - Only run for changes in the `/web` path.
  - Add to all versioned branches

### Teleport.e repository

[repository](https://github.com/gravitational/teleport.e)

#### Actions

- [ ] Clone the [Webapps.e repository](https://github.com/gravitational/webapps.e)
      into the Teleport.e root. [Maintaining their respective git histories](https://stackoverflow.com/questions/13040958/merge-two-git-repositories-without-breaking-file-history)
  - [ ] This will need to be done for every respective version branch (v9, v10, v11)
- [ ] Only require teleport build processes to run on teleport paths and the
      webapp ones to run on the webapp paths
-
- [ ] Archive webapps.e repository

#### CI jobs

- **Teleport-E-Lint (ci-account)**
- **Teleport-E-IntegrationTest**
- **Teleport-E-Test-Linux (ci-account)**
  - Disable for changes exclusively made in the `/web` path.
- **Assign / Auto Request Review**
  - Create a list of full stack engineers and have it pick from this list for
    changes made to the `/web` path.
- **Check / Checking reviewers**
  - Continue to require 2 reviewers for changes in the `web` projects.
- **CodeQL / Analyze (javascript) (pull_request)**
  - Ensure that it's running for changes in `/web` path.
- **Code scanning results / CodeQL**
  - No changes
- **webapps-build**
  - Instead of having this job build webassets and push it, it should ensure
    that it can be built.
- **webapps-test**
  - Migrate from the webapps repository to the teleport repository
  - Only run for changes in the `/web` path.
  - Add to all versioned branches
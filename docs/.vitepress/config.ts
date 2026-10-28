import { defineConfig } from "vitepress";

// The site is published to GitHub Pages at https://svnix-solutions.github.io/ogit/
// from main (.github/workflows/docs.yaml); DOCS_BASE overrides the base path.
export default defineConfig({
  title: "oGit",
  description: "A self-hosted Git server: repositories, pull requests, issues, wikis, Actions, packages and more.",
  base: process.env.DOCS_BASE ?? "/ogit/",
  cleanUrls: true,
  // Guides link to the oGit running on your machine (http://localhost:8080).
  ignoreDeadLinks: "localhostLinks",
  srcExclude: ["README.md"],
  themeConfig: {
    nav: [
      { text: "Using oGit", link: "/user/", activeMatch: "/user/" },
      { text: "Administration", link: "/admin/", activeMatch: "/admin/" },
      { text: "Guides", link: "/guides/", activeMatch: "/guides/" },
    ],
    sidebar: {
      "/user/": [
        {
          text: "Using oGit",
          items: [
            { text: "Overview", link: "/user/" },
            { text: "Your account", link: "/user/account" },
            { text: "Git over HTTP and SSH", link: "/user/git" },
          ],
        },
        {
          text: "Repositories",
          items: [
            { text: "Repositories", link: "/user/repositories" },
            { text: "Groups", link: "/user/groups" },
            { text: "Forks", link: "/user/forks" },
            { text: "Mirroring and imports", link: "/user/mirroring" },
            { text: "Git LFS", link: "/user/lfs" },
          ],
        },
        {
          text: "Collaboration",
          items: [
            { text: "Pull requests", link: "/user/pull-requests" },
            { text: "Branch protection", link: "/user/branch-protection" },
            { text: "Issues", link: "/user/issues" },
            { text: "Wikis", link: "/user/wikis" },
            { text: "Notifications", link: "/user/notifications" },
          ],
        },
        {
          text: "Shipping",
          items: [
            { text: "Actions", link: "/user/actions" },
            { text: "Releases", link: "/user/releases" },
            { text: "Packages", link: "/user/packages" },
          ],
        },
      ],
      "/admin/": [
        {
          text: "Running oGit",
          items: [
            { text: "Overview", link: "/admin/" },
            { text: "Installation", link: "/admin/install" },
            { text: "Configuration", link: "/admin/configuration" },
            { text: "Database", link: "/admin/database" },
            { text: "Storage", link: "/admin/storage" },
            { text: "Behind a reverse proxy", link: "/admin/reverse-proxy" },
          ],
        },
        {
          text: "Accounts and security",
          items: [
            { text: "Registration and email", link: "/admin/registration" },
            { text: "Two-factor authentication", link: "/admin/two-factor" },
            { text: "Single sign-on", link: "/admin/sso" },
            { text: "Rate limits", link: "/admin/rate-limits" },
            { text: "Site administration", link: "/admin/administration" },
          ],
        },
        {
          text: "Operations",
          items: [
            { text: "Upgrades", link: "/admin/upgrades" },
            { text: "Backups", link: "/admin/backups" },
            { text: "Logging and monitoring", link: "/admin/monitoring" },
          ],
        },
      ],
      "/guides/": [
        {
          text: "Guides",
          items: [
            { text: "All guides", link: "/guides/" },
            { text: "Try oGit with Docker Compose", link: "/guides/try-it" },
            { text: "Run oGit with Docker and PostgreSQL", link: "/guides/docker-postgres" },
            { text: "Sign in with Keycloak", link: "/guides/sso-keycloak" },
            { text: "Back up and restore", link: "/guides/backup-restore" },
            { text: "Migrate from GitHub", link: "/guides/migrate-from-github" },
            { text: "Run Actions on your own runner", link: "/guides/run-actions" },
            { text: "Publish a container image", link: "/guides/publish-a-container" },
          ],
        },
      ],
    },
    search: { provider: "local" },
    socialLinks: [{ icon: "github", link: "https://github.com/svnix-solutions/ogit" }],
    editLink: { pattern: "https://github.com/svnix-solutions/ogit/edit/main/docs/:path", text: "Edit this page" },
    outline: [2, 3],
  },
});

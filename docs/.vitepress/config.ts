import { defineConfig } from "vitepress";

// The site is published to GitHub Pages at https://svnix-solutions.github.io/ogit/
// from main (.github/workflows/docs.yaml); DOCS_BASE overrides the base path.
export default defineConfig({
  title: "oGit",
  description: "A self-hosted Git server: repositories, pull requests, issues, wikis, Actions, packages and more.",
  base: process.env.DOCS_BASE ?? "/ogit/",
  cleanUrls: true,
  srcExclude: ["README.md"],
  themeConfig: {
    nav: [
      { text: "Administration", link: "/admin/", activeMatch: "/admin/" },
      { text: "Guides", link: "/guides/", activeMatch: "/guides/" },
    ],
    sidebar: {
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
            { text: "Run oGit with Docker and PostgreSQL", link: "/guides/docker-postgres" },
            { text: "Sign in with Keycloak", link: "/guides/sso-keycloak" },
            { text: "Back up and restore", link: "/guides/backup-restore" },
          ],
        },
      ],
    },
    search: { provider: "local" },
    socialLinks: [{ icon: "github", link: "https://github.com/svnix-solutions/ogit" }],
    editLink: { pattern: "https://github.com/svnix-solutions/ogit/edit/refactor/docs/:path", text: "Edit this page" },
    outline: [2, 3],
  },
});

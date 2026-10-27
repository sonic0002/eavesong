# Eavesong

*Eaves* as in eavesdropping, the talk you overhear: Eavesong listens for what people say about your product.

Eavesong watches the places people talk about products (Hacker News, Reddit, Bluesky, X, YouTube, GitHub and more) for posts about your product and your competitors. It puts them in one inbox, drafts replies with your own AI provider when a reply would help, and reports how often your product comes up compared with your competitors.

It runs on your own server. Your tokens, your data and your AI prompts stay there.

**Eavesong only reads.** It never posts, comments, votes or replies for you. Drafts are suggestions you copy and post yourself, which is also what keeps your accounts in good standing on communities that ban automated posting.

## What it does

- **Inbox:** every post or comment that links your domain or mentions your product name, with where it was posted, who wrote it, and a link to reply. Mark items handled or ignored.
- **Reply drafts:** for each new mention the AI decides whether a reply is worth sending (most mentions don't need one), drafts a short answer when it is, and flags bug reports and complaints for a personal look.
- **Competitors:** add competitor names, and their mentions are collected and labelled separately.
- **Search:** with a read-only Google Search Console key, each project gets a Search tab with clicks, impressions, the searches it appears for, its top pages, searches where it is almost on page one, and sitemap status. Refreshed daily. A "What to do next" list turns the numbers into specific actions, each naming the search and the page: titles that rank but don't get clicked, pages close to page one, searches with no page that answers them, pages competing for the same search, and searches rising or falling. With an AI provider set, "Ask AI for a plan" writes them up as a prioritised plan. With a Bing Webmaster Tools API key too, the Search tab switches between Google and Bing; each engine keeps its own numbers, history, recommendations and plan. Bing groups searches by week and has no search-by-page breakdown, so its "pages competing" and "page about something else" findings are not made.
- **Traffic:** with a read-only Google Analytics 4 key, each project gets a Traffic tab: visitors, visits and page views against the 28 days before, visitors per day, where visitors came from, landing pages and the most read pages. "From the places Eavesong listens" puts the visits from Hacker News, Reddit, Bluesky and the other sources next to the mentions Eavesong found there, which shows where people talk about you without linking. The same service-account key as Search Console works; it needs the Google Analytics Admin and Data APIs enabled and Viewer access to the property. Each project is matched to the property whose web data stream measures its domain.
- **Report:** mentions by source, share of voice between your product and competitors, and the items that needed a reply, over 7, 30 or 90 days.
- **Your own accounts:** posts by your own usernames are recognised and kept out of the inbox.
- **Notifications:** opt in to alerts for mentions needing attention, competitor mentions and sitemap errors through Slack, Discord or a generic webhook. Daily and weekly summaries can use the same destinations.

## Run it

With Docker:

```sh
cp .env.example .env    # set EAVESONG_PASSWORD at least
docker compose up -d
```

Open http://127.0.0.1:8080, sign in with any username and your password, and create a project.
The first project leads through optional account and AI setup. Use **Settings → Move settings** to export project settings without credentials and import them on another installation.

From source (Go 1.26 or newer):

```sh
go build ./cmd/eavesong
EAVESONG_PASSWORD=change-me ./eavesong serve
```

`./eavesong check` runs one check of every project and exits, for use from cron.

## Configuration

Choose the AI provider for reply drafts (Anthropic, DeepSeek, Gemini, or any OpenAI-compatible API including a local model) and connect accounts on the **Settings** page (top right). You can connect several accounts per platform. API keys and tokens are encrypted before they are stored and are never shown again; leave a field empty to keep the saved value. Changes apply from the next check, with no restart. **Send a test draft** checks the key and model.

The **Notifications** section lets you connect Slack, Discord or a generic webhook, then choose which events should alert you. Daily digests run after 08:00 UTC; weekly reports run on Mondays after 08:00 UTC during the next scheduled check. The weekly report includes mention counts, share of voice and available Search Console and Bing Webmaster Tools totals. Eavesong records successful deliveries so each event is sent once per destination.

The encryption key lives in a file next to the database (`eavesong.db.key`, created on first run, readable only by its owner). Back it up with the database: without it, saved keys cannot be read and have to be entered again. To manage the key yourself, set `EAVESONG_SECRET_KEY` to 32 random bytes, base64-encoded (`openssl rand -base64 32`).

Environment variables (see `.env.example`) set the server itself, and give defaults for anything not saved on the Settings page.

| Variable | Default | Purpose |
|---|---|---|
| `EAVESONG_PASSWORD` | none | Password for the web interface. Required unless Eavesong only listens on localhost. |
| `EAVESONG_ADDR` | `127.0.0.1:8080` | Address to listen on. |
| `EAVESONG_DB` | `eavesong.db` | SQLite database file. |
| `EAVESONG_INTERVAL` | `30m` | How often to check (at least `5m`). |
| `EAVESONG_X_DAILY_CAP` | `100` | Most X posts read per day (X charges per read); also on the Settings page. |
| `EAVESONG_SECRET_KEY` | key file next to the database | Encryption key for saved API keys and tokens. |
| `EAVESONG_AI_PROVIDER` | `none` | `anthropic`, `deepseek`, `gemini`, `openai` (any OpenAI-compatible API, including Ollama) or `none`. |
| `EAVESONG_AI_MODEL` | `claude-sonnet-5-5` for Anthropic, `deepseek-flash` for DeepSeek, `gemini-3.8-flash` for Gemini | Model name. |
| `EAVESONG_AI_BASE_URL` | `https://api.openai.com/v1` | For `openai`: the API address, e.g. `http://localhost:11434/v1` for Ollama. |
| `ANTHROPIC_API_KEY`, `DEEPSEEK_API_KEY`, `GEMINI_API_KEY`, `OPENAI_API_KEY` | none | Your own keys. |
| `BLUESKY_HANDLE`, `BLUESKY_APP_PASSWORD` | none | Enables Bluesky. Use an app password, never your main password. |
| `GITHUB_TOKEN` | none | Optional; raises GitHub's search limit. Read-only access is enough. |
| `REDDIT_INBOX_FEED` | none | Optional private feed of replies to your own Reddit posts and comments, from reddit.com/prefs/feeds (use the RSS link on www.reddit.com). Treat it like a password. |

## Sources

| Source | What it finds | Needs |
|---|---|---|
| Hacker News | Stories and comments from the last 7 days | nothing (public Algolia API) |
| Reddit | Posts linking your domains, keyword search, and replies in the inbox of every account you connect | nothing; Reddit rate-limits feeds, so some checks may be skipped and retried |
| Lobsters | Stories linking your domains | nothing |
| GitHub | Issues and pull requests from the last 7 days | nothing; tokens raise the limit, and with several the next is used when one is limited |
| Bluesky | Posts linking your domains or naming your terms, plus replies, mentions and quotes of every connected account | at least one handle and app password |
| Stack Overflow | Questions from the last 30 days that mention your terms | nothing; a free key from stackapps.com raises the quota (without one it checks every two hours) |
| X | Recent posts mentioning your terms, and posts mentioning your accounts | your own X API bearer token; X charges per post read, so reads stop at a daily limit you set (default 100) and it checks at most every two hours |
| Mastodon | Public posts across the network, and each account's mentions | an access token with the read scope from your server |
| YouTube | New videos about your product | a YouTube Data API key; checked every six hours to stay inside the free quota |
| Facebook Pages | Comments on recent posts of Pages you manage (Meta does not allow searching public posts) | a Page access token and the Page ID |
| Google Alerts | Blogs, news and forums, through alerts delivered as RSS | the alert's RSS link from google.com/alerts |
| Google Search Console | Search data for the property matching each project's websites (Search tab) | a service-account JSON key, added to the property as a Restricted user |
| Bing Webmaster Tools | Bing search data for the verified site matching each project's websites (Search tab) | an API key from Bing Webmaster Tools, Settings, API access; it reads every site its account has verified |
| Product Hunt | Launches whose name, tagline or description mention your terms, and their comments | a developer token from producthunt.com/v2/oauth/applications; Product Hunt has no search, so it reads every launch of the last two days, at most once an hour |
| DEV (dev.to) | Comments on the published articles of every connected account (DEV has no public search) | an API key from dev.to/settings/extensions |

Reddit no longer approves new API apps for this kind of use, so Eavesong uses Reddit's public feeds at a modest rate and does not scrape. Posts by bots such as `dependabot[bot]` and `renovate[bot]` are ignored.

## Tips

- Pick keywords that are unlikely to mean something else. A product name that is also a common word or a code identifier will match unrelated posts; prefer your domain and distinctive spellings.
- Competitors are best given as product names, not as domains of large sites that appear in every dependency update.

## Privacy and security

- API keys and tokens saved on the Settings page are encrypted with AES-256-GCM; a copy of the database alone does not reveal them.
- Data stays in the SQLite file on your server. Outbound requests go to the sources above, the AI provider you choose, and any notification destinations you configure.
- The AI provider receives mention text and the project context you provide for drafts; if you enable linked-page reading, the retrieved page text is also included. With a local model, draft prompts stay on your machine.
- The web interface sends strict security headers, refuses cross-site form posts, and refuses to listen beyond localhost without a password. Put it behind a TLS reverse proxy before exposing it to the internet.

## License

AGPL-3.0. You can run, change and self-host Eavesong freely; if you offer a modified version to others as a hosted service, you must share your changes under the same license.

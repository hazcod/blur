# Blur website

Static site for https://blur.hazcod.com. Plain HTML and CSS, no build step, no JavaScript, no external requests, no analytics or cookies.

Files: `index.html`, `style.css`, `favicon.svg`, `robots.txt`, `CNAME`.

## Hosting

Upload this directory to any static host (Netlify, Cloudflare Pages, S3, nginx). Point the document root at `website/`.

### GitHub Pages

1. Publish the contents of `website/` to a branch or Pages artifact (for example with a workflow that uploads `website/` as the Pages artifact).
2. In the repository settings, set the custom domain to `blur.hazcod.com`. The `CNAME` file already contains it.
3. Add a DNS `CNAME` record for `blur` pointing to `hazcod.github.io`.

### Preview locally

    python3 -m http.server --directory website 8080

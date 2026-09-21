"""Provision the local owner and storefront site through the public Rybbit API."""
import http.cookiejar
import json
import os
from pathlib import Path
import urllib.error
import urllib.request

root = Path(os.environ["RYBBIT_STATE"])
base = "http://localhost:" + os.environ["RYBBIT_PORT"]
class LocalCookiePolicy(http.cookiejar.DefaultCookiePolicy):
    # Browsers treat localhost as trustworthy for Secure cookies. Mirror that
    # exception only for this exact local origin; never relax it for remote URLs.
    def return_ok_secure(self, cookie, request):
        return request.full_url.startswith(base + "/") or super().return_ok_secure(cookie, request)


client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar(policy=LocalCookiePolicy())))


def api(path, body=None):
    request = urllib.request.Request(base + path, data=None if body is None else json.dumps(body).encode(), headers={"Content-Type": "application/json", "Origin": base})
    with client.open(request, timeout=30) as response:
        return json.load(response)


def login():
    admin = json.loads((root / "admin.json").read_text())
    try:
        api("/api/auth/sign-in/email", admin)
    except urllib.error.HTTPError as error:
        if error.code != 401:
            raise
        api("/api/auth/sign-up/email", admin)


def bootstrap():
    login()
    organizations = api("/api/auth/organization/list")
    organization = next((item for item in organizations if item["slug"] == "marketmesh-local"), None)
    if organization is None:
        organization = api("/api/auth/organization/create", {"name": "MarketMesh local", "slug": "marketmesh-local"})
    path = "/api/organizations/" + organization["id"] + "/sites"
    sites = api(path)
    if isinstance(sites, dict):
        sites = sites.get("sites", sites.get("data", []))
    site = next((item for item in sites if item["domain"] == "marketmesh.localhost"), None)
    if site is None:
        site = api(path, {"name": "MarketMesh local storefront", "domain": "marketmesh.localhost", "public": False, "blockBots": False, "saltUserIds": True, **{key: False for key in ("sessionReplay", "webVitals", "trackErrors", "trackOutbound", "trackUrlParams", "trackInitialPageView", "trackSpaNavigation", "trackIp", "trackButtonClicks", "trackCopy", "trackFormInteractions")}})
    (root / "site-id").write_text(str(site["siteId"]) + "\n")
    (root / "site-id").chmod(0o600)
    print("Локальный сайт Rybbit готов; site ID: " + str(site["siteId"]))


if __name__ == "__main__":
    try:
        bootstrap()
    except (urllib.error.URLError, KeyError, ValueError) as error:
        raise SystemExit("Bootstrap Rybbit не завершён: " + type(error).__name__)

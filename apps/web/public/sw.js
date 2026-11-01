// Monetag site verification.
//
// Monetag's recommended verification is a file at the site root, and this is
// that file, served verbatim as they issued it. It is ALSO the service worker
// their Multitag registers to deliver push notifications -- which is why their
// dashboard says not to delete it once verified.
//
// Nothing in this application registers it. A service worker at `/sw.js`
// claims the whole origin: registering this one would put a third party in
// front of every request the site makes and would ask every reader for
// notification permission on arrival. That is a product decision about push,
// not a step in verifying a domain, and it has not been taken.
self.options = {
    "domain": "3nbf4.com",
    "zoneId": 11943563
}
self.lary = ""
importScripts('https://3nbf4.com/act/files/service-worker.min.js?r=sw')

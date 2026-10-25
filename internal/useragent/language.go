package useragent

// baseLanguage is the base language of BrowserLanguage.
const baseLanguage = "en"

// BrowserLanguage is the language that Chrome gets through --accept-lang.
const BrowserLanguage = baseLanguage + "-US"

// AcceptLanguage is the accept-language value that Chrome sends for
// BrowserLanguage. Chrome adds the base language of a regional language at q=0.9.
const AcceptLanguage = BrowserLanguage + "," + baseLanguage + ";q=0.9"

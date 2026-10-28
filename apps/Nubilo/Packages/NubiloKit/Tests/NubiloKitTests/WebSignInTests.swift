import XCTest
@testable import NubiloKit

final class WebSignInTests: XCTestCase {
    func testParsesLinkAndTypedCodes() {
        let link = WebSignInLink(parsing: "nubilo://web-login?url=https%3A%2F%2Fnubilo.exe.xyz&code=ABCD-EFGH")
        XCTAssertEqual(link?.code, "ABCD-EFGH")
        XCTAssertEqual(link?.server?.host(), "nubilo.exe.xyz")
        XCTAssertEqual(WebSignInLink(parsing: " abcd efgh ")?.code, "ABCD-EFGH")
        XCTAssertEqual(WebSignInLink(parsing: "abcdefgh")?.code, "ABCD-EFGH")
        XCTAssertNil(WebSignInLink(parsing: "ABCD-EFG0"), "0 is not in the alphabet")
        XCTAssertNil(WebSignInLink(parsing: "ABC"))
        XCTAssertNil(WebSignInLink(parsing: "nubilo://setup?url=https://x&invite=y"))
        XCTAssertNil(WebSignInLink(parsing: "nubilo://web-login?url=http://x&code=ABCDEFGH"), "plain http server")
    }

    func testDescribesBrowsers() {
        XCTAssertEqual(describeBrowser("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15"), "Safari on Mac")
        XCTAssertEqual(describeBrowser("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36 Edg/130.0"), "Edge on Windows")
        XCTAssertEqual(describeBrowser("curl/8"), "A browser")
    }
}

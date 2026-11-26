package me.itsjustin.clrd

import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.test.core.app.ApplicationProvider
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okio.Buffer
import org.json.JSONObject
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import java.util.Base64

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class SenderIconsTest {
    @get:Rule val rule = createComposeRule()

    @Test fun aConfiguredIconShowsOnlyOnItsSender() {
        MockWebServer().use { server ->
            server.start()
            val png = Base64.getDecoder().decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")
            server.enqueue(MockResponse().setBody(Buffer().write(png)))
            val app = ApplicationProvider.getApplicationContext<App>()
            val state = mutableStateOf<List<JSONObject>?>(listOf(
                JSONObject("""{"id":"one","name":"Builds","rules":{"icon":"${server.url("/icon.png")}"}}"""),
                JSONObject("""{"id":"two","name":"Backups","rules":{}}"""),
            ))
            rule.setContent { SenderSettings(app, 0, state) }
            rule.waitUntil(5000) {
                rule.onAllNodesWithTag("sender-icon-one", useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty()
            }
            rule.onNodeWithTag("sender-icon-one", useUnmergedTree = true).assertIsDisplayed()
            rule.onNodeWithTag("sender-icon-two", useUnmergedTree = true).assertDoesNotExist()
            rule.onNodeWithText("Backups").assertIsDisplayed()
        }
    }
}

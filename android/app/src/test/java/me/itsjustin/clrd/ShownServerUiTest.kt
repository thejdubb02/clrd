package me.itsjustin.clrd

import android.content.Intent
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assertIsSelected
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isSelectable
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performTextReplacement
import androidx.test.core.app.ApplicationProvider
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/** The screens after a switch (#78): a job queued before it writes to the server it was for, and a tray tap lands on the inbox. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class ShownServerUiTest {
    @get:Rule val rule = createComposeRule()
    private val app = ApplicationProvider.getApplicationContext<App>()
    private val server = MockWebServer()
    private lateinit var b: Account

    @Before fun setUp() {
        server.dispatcher = object : Dispatcher() { override fun dispatch(request: RecordedRequest) = MockResponse().setBody("{}") }
        server.start()
        app.account.serverUrl = server.url("/").toString()
        app.account.token = "ta"
        b = app.addAccount("https://b.example", "tb")
    }

    @After fun tearDown() {
        b.db.close()
        server.shutdown()
    }

    private fun s(id: Int) = app.getString(id)
    private fun drain() = app.io.submit {}.get(10, TimeUnit.SECONDS)

    /** Holds [App.io] while [tap] queues its job and the other server is shown, then lets it run. */
    private fun switchBeforeTheJob(tap: () -> Unit) {
        val gate = CountDownLatch(1)
        app.io.execute { gate.await(10, TimeUnit.SECONDS) }
        tap()
        app.show(b.id)
        gate.countDown()
        drain()
    }

    @Test fun saveAndTestWritesTheServerItWasTappedFor() {
        rule.setContent { SettingsScreen(app, null, 0) {} }
        switchBeforeTheJob { rule.onNodeWithText(s(R.string.save_and_test)).performScrollTo().performClick() }
        assertEquals("https://b.example", b.serverUrl)
        assertEquals("tb", b.token)
        assertEquals(server.url("/").toString().trimEnd('/'), app.account.serverUrl.trimEnd('/'))
    }

    @Test fun aSoundPickedBeforeASwitchIsTheFirstServers() {
        rule.setContent { SettingsScreen(app, null, 0) {} }
        val radios = rule.onAllNodes(SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.RadioButton))
        val name = Sounds.NAMES.first { it != Sounds.DEFAULT }
        switchBeforeTheJob { radios[Sounds.NAMES.indexOf(name)].performScrollTo().performClick() }
        assertEquals(name, app.account.sound)
        assertEquals(Sounds.DEFAULT, b.sound)
    }

    /** Account 0's own address, which answers: without the check the test passes and its token is replaced. */
    @Test fun addingAServerThePhoneHasSavesNothing() {
        val url = serverAddress(server.url("/").toString())
        rule.setContent { SettingsScreen(app, null, 0) {} }
        rule.onNodeWithText(s(R.string.server_add)).performScrollTo().performClick()
        rule.onAllNodesWithText(s(R.string.server_url))[1].performScrollTo().performTextReplacement(url)
        rule.onAllNodesWithText(s(R.string.token))[1].performScrollTo().performTextReplacement("other")
        rule.onAllNodesWithText(s(R.string.save_and_test))[1].performScrollTo().performClick()
        drain()
        rule.onNodeWithText(app.getString(R.string.server_already, url)).assertExists()
        assertEquals("ta", app.account.token)
        assertEquals(2, app.accounts.size)
    }

    @Test fun aTrayOpenLandsOnTheInbox() {
        val opens = mutableStateOf(0)
        rule.setContent { Home(app, null, 0, opens.value) {} }
        rule.onNode(hasText(s(R.string.tab_settings)) and isSelectable()).performClick()
        rule.onNode(hasText(s(R.string.tab_settings)) and isSelectable()).assertIsSelected()
        opens.value++
        rule.onNode(hasText(s(R.string.tab_inbox)) and isSelectable()).assertIsSelected()
    }

    /** Account 0's tray tap has no data, as before #78, and still shows account 0; the launcher and sync notice leave the choice. */
    @Test fun whichTapsChooseTheServer() {
        val tray = Intent(app, MainActivity::class.java).setFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP)
        assertEquals(0, openedAccount(tray))
        assertEquals(b.id, openedAccount(Intent(tray).setData(android.net.Uri.parse("${Tray.OPEN}?account=${b.id}"))))
        assertNull("the launcher", openedAccount(Intent(Intent.ACTION_MAIN).setClass(app, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP)))
        assertNull("the sync notice", openedAccount(Intent(app, MainActivity::class.java)))
        assertNull("a pairing link", openedAccount(Intent(Intent.ACTION_VIEW, android.net.Uri.parse("clrd://pair?x=1"))))
    }
}

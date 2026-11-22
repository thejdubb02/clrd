package me.itsjustin.clrd

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Test

/** The Sent message (#532): shown once per send, and again when a rotation cut it short. */
class SenderTest {
    @Test fun aSentMessageCutShortShowsAgain() = runBlocking {
        val s = Sender { it.run() }
        var shows = 0
        s.confirm { shows++ }
        assertEquals(0, shows) // nothing sent yet, as in a fresh process
        s.send(post = {}, why = { "" })
        val never = CompletableDeferred<Unit>()
        launch(start = CoroutineStart.UNDISPATCHED) { s.confirm { shows++; never.await() } }.cancel() // the screen turned mid-show
        s.confirm { shows++ }
        assertEquals(2, shows) // the rebuilt screen shows it again
        s.confirm { shows++ }
        assertEquals(2, shows) // and only once after it finished
    }
}

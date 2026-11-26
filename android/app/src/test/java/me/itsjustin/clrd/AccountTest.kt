package me.itsjustin.clrd

import android.content.Context
import androidx.room.Room
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** More than one server (#78): account 0 is the install as it was, and each account keeps its own settings and rows. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34])
class AccountTest {
    private val ctx = ApplicationProvider.getApplicationContext<App>()
    private fun row(id: String) = Notif(id, "t", "T", "m", 3, "", null, "[]", Notif.ACTIVE, null, null, 1, 1, 1)

    @Test fun anUpdatedInstallReadsItsServerAsAccountZero() {
        // What a build from before #78 left on the phone: the "clrd" file and "clrd.db".
        ctx.getSharedPreferences("clrd", Context.MODE_PRIVATE).edit()
            .putString("url", "https://home.example").putString("token", "tk").putString("epoch", "e1").commit()
        Room.databaseBuilder(ctx, Db::class.java, "clrd.db").allowMainThreadQueries().build().apply { dao().put(row("old")); close() }

        // The app itself, not a hand-made account, so the wiring the phone runs is what is checked.
        assertEquals("https://home.example", ctx.serverUrl)
        assertEquals("tk", ctx.token)
        assertEquals("e1", ctx.epoch)
        assertTrue(ctx.configured)
        assertEquals("old", bg { ctx.db.dao().get("old")?.id })
    }

    @Test fun accountsDoNotSeeEachOther() {
        ctx.stack = true
        val home = Account(ctx, 0).apply { serverUrl = "https://home.example"; token = "a" }
        val work = Account(ctx, 1).apply { serverUrl = "https://work.example"; token = "b" }
        bg { work.db.dao().put(row("w")) }

        assertEquals("https://home.example", Account(ctx, 0).serverUrl)
        assertEquals("https://work.example", Account(ctx, 1).serverUrl)
        assertNull(bg { home.db.dao().get("w") })
        assertTrue(ctx.prefs.getBoolean("stack", false)) // phone choices stay in the "clrd" file, not a second server's
        assertTrue(!work.prefs.contains("stack"))
        home.db.close(); work.db.close()
    }
}

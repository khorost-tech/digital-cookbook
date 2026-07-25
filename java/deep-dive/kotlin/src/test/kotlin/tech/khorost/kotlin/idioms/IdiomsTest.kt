package tech.khorost.kotlin.idioms

import kotlinx.coroutines.runBlocking
import kotlin.test.Test
import kotlin.test.assertEquals

class IdiomsTest {

    @Test
    fun `elvis operator falls back to default on null chain`() {
        assertEquals("Привет, аноним!", greet(null))
        assertEquals("Привет, аноним!", greet(UserProfile(2, null, "a@b.c")))
        assertEquals("Привет, Александр!", greet(UserProfile(1, "Александр", "a@b.c")))
    }

    @Test
    fun `data class copy changes only the given field`() {
        val article = Article("slug", "title", listOf("tag"), views = 5)
        val copy = article.copy(views = 6)
        assertEquals(article.slug, copy.slug)
        assertEquals(6, copy.views)
        assertEquals(article, article.copy())
    }

    @Test
    fun `extension function slugifies a title`() {
        assertEquals("wal-3", "  WAL #3  ".toSlug())
    }

    @Test
    fun `list extension functions aggregate and filter`() {
        val articles = listOf(
            Article("a", "A", listOf("wal"), views = 10),
            Article("b", "B", listOf("indexes"), views = 20),
        )
        assertEquals(30, articles.totalViews())
        assertEquals(listOf("a"), articles.byTag("wal").map { it.slug })
    }

    @Test
    fun `sealed hierarchy is exhaustively handled`() {
        val published = PublishResult.Published("s", "https://khorost.tech/s/")
        assertEquals("опубликовано: https://khorost.tech/s/", describe(published))
    }

    @Test
    fun `structured concurrency loads a card with both fields populated`() = runBlocking {
        val card = loadArticleCard("wal-3")
        assertEquals("wal-3", card.slug)
        assertEquals("Заголовок для wal-3", card.title)
    }
}

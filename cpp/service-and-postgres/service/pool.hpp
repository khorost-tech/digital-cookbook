#pragma once
#include <condition_variable>
#include <cstddef>
#include <functional>
#include <memory>
#include <mutex>
#include <stdexcept>
#include <utility>
#include <vector>

// ОГРАНИЧЕННЫЙ НАБОР ПОСТОЯННЫХ СОЕДИНЕНИЙ — предмет оси 2.
//
// ЗАЧЕМ ОН ПОЯВИЛСЯ. До этой задачи пула не было ни у одного из трёх
// слоёв: `query()` открывал соединение на каждый запрос и закрывал его
// вместе с ответом. Это ИЗМЕРЕНО, а не прочитано в коде: залп
// одновременных /q показывал в pg_stat_activity ровно столько
// соединений, сколько запросов было В ПОЛЁТЕ, а после залпа — ноль (см.
// expected.json, блок pool). Именно в полёте, а не «сколько рабочих
// единиц»: у слоя, чей потолок ниже числа единиц, соединений было
// меньше — перезамер на том же коммите дал у crow семь при восьми
// единицах. При таком устройстве контроль оси 2 («пул
// на 1 против пула на 8») был бы декоративен: подключались бы заново при
// любом значении ручки. Ровно поэтому обвязка замера до сих пор
// ОТВЕРГАЛА довод pool — ручка, не подключённая к предмету.
//
// ПОЧЕМУ ПУЛ ОДИН НА ТРИ СЛОЯ И НАПИСАН РУКАМИ. Спека предвидела
// асимметрию: у Drogon пул свой, у Beast и Crow его нет. Собственный пул
// Drogon в образе действительно есть (libdrogon слинкована с libpq), но
// он ходит в базу СВОИМ клиентом, а два других слоя — через libpqxx. Ось
// сравнивала бы тогда драйверы, а не слои, и рухнуло бы главное правило
// стенда: различаться обязан только слой HTTP. Поэтому пул здесь один и
// тот же для всех трёх, а собственный пул Drogon замерен ОТДЕЛЬНОЙ
// строкой фикстуры и в ось не входит.
//
// ПОЧЕМУ ШАБЛОН, А НЕ КЛАСС ПОВЕРХ pqxx::connection. Правила выдачи —
// потолок, возврат слота при исключении, освобождение порванного
// соединения — можно проверить ИСПОЛНЕНИЕМ, и проверить без базы:
// подставив вместо соединения счётчик. Класс, прибитый к pqxx, требовал
// бы для этого живого PostgreSQL, то есть проверялся бы только там, где
// проверять дороже всего. Заголовок намеренно не включает pqxx.
//
// ЧЕГО ЗДЕСЬ НЕТ. Ни тайм-аутов, ни проверки соединения на живость перед
// выдачей, ни роста и усыхания набора. Каждое из этого — своя величина,
// которую ось не меряет; лишняя ручка тут добавила бы переменную,
// объясняющую расхождение вторым способом.
template <typename Connection>
class ConnectionPool {
  public:
    using Factory = std::function<std::unique_ptr<Connection>()>;

    // Выданное соединение. Слот возвращается В ДЕСТРУКТОРЕ — то есть и
    // при выходе исключением тоже.
    //
    // ПОЧЕМУ ЭТО ГЛАВНАЯ СТРОКА ФАЙЛА. Обработчик умеет выйти
    // исключением: база отказала, запрос кривой, соединение порвано.
    // Слот, возвращаемый последней строкой тела, в этом случае не
    // вернулся бы — и пул на 8 после восьми отказов раздал бы всё и
    // встал бы навсегда, а счёт соединений в СЛЕДУЮЩЕМ замере считал бы
    // остаток предыдущего. Утечка исказила бы не тот замер, в котором
    // случилась, — то есть выглядела бы свойством слоя.
    class Lease {
      public:
        Lease(ConnectionPool* owner, std::unique_ptr<Connection> held)
            : owner_(owner), held_(std::move(held)) {}

        ~Lease() {
            if (owner_ == nullptr) return;
            if (discarded_ || held_ == nullptr) {
                owner_->drop_slot();
            } else {
                owner_->give_back(std::move(held_));
            }
        }

        Lease(const Lease&) = delete;
        Lease& operator=(const Lease&) = delete;
        Lease(Lease&& other) noexcept
            : owner_(other.owner_), held_(std::move(other.held_)),
              discarded_(other.discarded_) {
            other.owner_ = nullptr;
        }
        Lease& operator=(Lease&&) = delete;

        Connection& operator*() const { return *held_; }
        Connection* operator->() const { return held_.get(); }

        // Соединение больше не годится: сервер разорвал его или отказал
        // так, что продолжать сеанс нельзя. Оно НЕ возвращается в набор,
        // а слот освобождается под новое подключение.
        //
        // Без этого порванное соединение вернулось бы в набор и отравило
        // бы все последующие запросы, взявшие тот же слот: замер видел
        // бы отказы там, где отказала одна попытка.
        void discard() { discarded_ = true; }

      private:
        ConnectionPool* owner_;
        std::unique_ptr<Connection> held_;
        bool discarded_ = false;
    };

    ConnectionPool(std::size_t size, Factory factory)
        : size_(size), factory_(std::move(factory)) {
        if (size_ == 0) {
            throw std::invalid_argument(
                "размер пула соединений обязан быть положительным: пул на "
                "ноль слотов не выдаст ни одного соединения и повиснет на "
                "первом же запросе");
        }
        if (!factory_) {
            throw std::invalid_argument(
                "пулу не дано, чем открывать соединение");
        }
    }

    // Соединение из набора: готовое, если есть, иначе новое — но не
    // больше `size_` штук разом.
    //
    // ЖДЁТ ЗДЕСЬ ТОТ, КТО ПОЗВАЛ. Это осознанный выбор, а не упущение.
    // Отдать ожидание слота планировщику корутин было бы можно только у
    // одного слоя из трёх, и тогда ось мерила бы разницу ожиданий, а не
    // размер пула. Ожидание слота занимает рабочую единицу так же, как
    // ожидание в базе (ось 1 замерила, что синхронный libpqxx занимает её
    // и у корутинного слоя) — и это не дефект, который стоит прятать, а
    // наблюдение, которое ось записывает числом: занятость при пуле на 1
    // лежит в фикстуре рядом со счётом соединений.
    Lease acquire() {
        std::unique_lock<std::mutex> lock(mutex_);
        free_.wait(lock, [this] { return !idle_.empty() || open_ < size_; });
        if (!idle_.empty()) {
            std::unique_ptr<Connection> held = std::move(idle_.back());
            idle_.pop_back();
            return Lease(this, std::move(held));
        }
        // Слот бронируется ДО подключения и под замком, а подключение
        // идёт уже без него: открытие соединения — поход в сеть, и
        // держать на нём общий замок значило бы выдавать по одному
        // соединению за раз независимо от размера пула. Тогда пул на 8
        // вёл бы себя как пул на 1 ровно в том замере, который их
        // различает.
        ++open_;
        lock.unlock();
        try {
            std::unique_ptr<Connection> made = factory_();
            if (made == nullptr) {
                throw std::runtime_error(
                    "пулу вернули пустое соединение вместо открытого");
            }
            return Lease(this, std::move(made));
        } catch (...) {
            drop_slot();
            throw;
        }
    }

    std::size_t size() const { return size_; }

    // Сколько соединений набор держит открытыми ПРЯМО СЕЙЧАС — считая
    // выданные. Это внутренний счёт, и на ось он не идёт: ось считает
    // соединения из самой базы. Здесь он нужен проверкам пула.
    std::size_t open() const {
        std::lock_guard<std::mutex> lock(mutex_);
        return open_;
    }

    std::size_t idle() const {
        std::lock_guard<std::mutex> lock(mutex_);
        return idle_.size();
    }

  private:
    void give_back(std::unique_ptr<Connection> held) {
        {
            std::lock_guard<std::mutex> lock(mutex_);
            idle_.push_back(std::move(held));
        }
        free_.notify_one();
    }

    void drop_slot() {
        {
            std::lock_guard<std::mutex> lock(mutex_);
            --open_;
        }
        free_.notify_one();
    }

    mutable std::mutex mutex_;
    std::condition_variable free_;
    const std::size_t size_;
    Factory factory_;
    std::vector<std::unique_ptr<Connection>> idle_;
    std::size_t open_ = 0;
};

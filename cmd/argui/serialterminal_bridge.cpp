#include "serialterminal_bridge.h"

#include <qtermwidget.h>

#include <QApplication>
#include <QByteArray>
#include <QEvent>
#include <QKeyEvent>
#include <QMutex>
#include <QMutexLocker>
#include <algorithm>
#include <cerrno>
#include <unistd.h>

class SerialTerminalWidget final : public QTermWidget {
public:
    explicit SerialTerminalWidget(QWidget *parent)
        : QTermWidget(0, parent) {
        QApplication::instance()->installEventFilter(this);
    }

protected:
    bool event(QEvent *event) override {
        if (claimsShortcut(event)) {
            event->accept();
            return true;
        }
        return QTermWidget::event(event);
    }

    bool eventFilter(QObject *watched, QEvent *event) override {
        auto *watchedWidget = qobject_cast<QWidget *>(watched);
        if (watchedWidget != nullptr
            && (watchedWidget == this || isAncestorOf(watchedWidget))
            && claimsShortcut(event)) {
            event->accept();
            return true;
        }
        return QTermWidget::eventFilter(watched, event);
    }

private:
    static bool claimsShortcut(QEvent *event) {
        if (event->type() != QEvent::ShortcutOverride) {
            return false;
        }
        const auto *keyEvent = static_cast<QKeyEvent *>(event);
        return keyEvent->key() == Qt::Key_W
            && keyEvent->modifiers() == Qt::ControlModifier;
    }
};

class SerialTerminal {
public:
    explicit SerialTerminal(QWidget *parent)
        : widget(new SerialTerminalWidget(parent)) {
        widget->setHistorySize(10000);
        widget->setScrollBarPosition(QTermWidget::ScrollBarRight);
        widget->setConfirmMultilinePaste(true);
        widget->startTerminalTeletype();
        slaveFd = widget->getPtySlaveFd();
        QObject::connect(widget, &QTermWidget::sendData, widget,
            [this](const char *data, int length) {
                QMutexLocker lock(&mutex);
                input.append(data, length);
            });
    }

    SerialTerminalWidget *widget;
    int slaveFd = -1;
    QMutex mutex;
    QByteArray input;
};

extern "C" void *serial_terminal_new(void *parent) {
    return new SerialTerminal(static_cast<QWidget *>(parent));
}

extern "C" void *serial_terminal_widget(void *terminal) {
    return static_cast<SerialTerminal *>(terminal)->widget;
}

extern "C" int serial_terminal_feed(void *terminal, const char *data, int length) {
    auto *bridge = static_cast<SerialTerminal *>(terminal);
    int written = 0;
    while (written < length) {
        const ssize_t count = ::write(bridge->slaveFd, data + written, static_cast<size_t>(length - written));
        if (count < 0 && errno == EINTR) {
            continue;
        }
        if (count <= 0) {
            return written > 0 ? written : -1;
        }
        written += static_cast<int>(count);
    }
    return written;
}

extern "C" int serial_terminal_read_input(void *terminal, char *data, int capacity) {
    auto *bridge = static_cast<SerialTerminal *>(terminal);
    QMutexLocker lock(&bridge->mutex);
    const int count = std::min(capacity, static_cast<int>(bridge->input.size()));
    if (count > 0) {
        std::copy_n(bridge->input.constData(), count, data);
        bridge->input.remove(0, count);
    }
    return count;
}

extern "C" void serial_terminal_clear(void *terminal) {
    static_cast<SerialTerminal *>(terminal)->widget->clear();
}

extern "C" void serial_terminal_focus(void *terminal) {
    static_cast<SerialTerminal *>(terminal)->widget->setFocus();
}

extern "C" void serial_terminal_copy(void *terminal) {
    static_cast<SerialTerminal *>(terminal)->widget->copyClipboard();
}

extern "C" void serial_terminal_paste(void *terminal) {
    static_cast<SerialTerminal *>(terminal)->widget->pasteClipboard();
}

extern "C" void serial_terminal_delete(void *terminal) {
    auto *bridge = static_cast<SerialTerminal *>(terminal);
    delete bridge->widget;
    delete bridge;
}

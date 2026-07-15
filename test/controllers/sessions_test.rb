require "test_helper"

class SessionsTest < ActionDispatch::IntegrationTest
  setup do
    @user = User.create!(email: "viewer@example.com", password: "s3cretpass")
  end

  test "renders the sign-in form" do
    get new_user_session_path

    assert_response :success
    assert_select "main.signin" do
      assert_select "form[action=?]", user_session_path do
        assert_select "input[name='user[email]'][required]"
        assert_select "input[name='user[password]'][required]"
        assert_select "input[name='user[remember_me]'][type=checkbox]"
      end
    end
  end

  test "signs in with valid credentials and remembers the user" do
    post user_session_path, params: {
      user: { email: "viewer@example.com", password: "s3cretpass", remember_me: "1" }
    }

    assert_redirected_to root_path
    assert cookies[:remember_user_token].present?
  end

  test "rejects invalid credentials" do
    post user_session_path, params: {
      user: { email: "viewer@example.com", password: "wrong" }
    }

    assert_response :unprocessable_entity
  end
end
